// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package firstmover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ernestrc/go-multierror"
	logd "github.com/ernestrc/logd-go/logging"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	storagerpcclient "github.com/unstablebuild/rune-go-sdk/api/storageapi/storagerpc"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagerpc/docpb"
	"github.com/unstablebuild/rune-go-sdk/retry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/localstorage/firstmover/pubsubpb"
	"unstable.build/rune/internal/localstorage/schemedoc"
	"unstable.build/rune/internal/localstorage/storagerpc"
)

// DefaultMaxMessageSize is the default maximum message size
// of published messages via Service.Publish. This can be
// overwritten via Config.MaxMessageSize.
const DefaultMaxMessageSize = 1024 * 1024 * 4

// ErrMessageTooLarge is returned in calls to Service.Publish
// when message is larger than MaxMessageSize.
var ErrMessageTooLarge = errors.New("message exceeds maximum size")

// testHookLeadBeforeResubscribe lets tests hold a freshly elected
// leader inside the window between unlocking the API and restoring
// the subscriptions recovered from its predecessor.
var testHookLeadBeforeResubscribe atomic.Pointer[func()]

// testHookSuppressBye lets tests reproduce a leader that disappears
// without the goodbye handshake, the way a crashed peer does.
var testHookSuppressBye atomic.Bool

// StorageFactory opens the backing storageapi.Service for a peer that
// has just won leader election. The leader owns the returned service
// for the duration of its leadership and closes it when leadership
// ends (graceful close, transient failure, or coup). Followers never
// invoke the factory because they proxy through the leader via gRPC.
//
// Returning the factory pattern lets backends that hold exclusive OS
// resources (e.g. bbolt's flock on rune.db) align acquisition with
// firstmover's "one active writer" invariant rather than racing for
// the file at construction time.
type StorageFactory func() (storageapi.Service, error)

// Service is a storageapi.Service that either acquires a lock
// by creating a unix socket at lockFile and exposes svc
// to RPC clients or if it fails to acquire lock, it will connect
// to the current leader via lockFile.
//
// If the leader is closed after this Service connects to it,
// or it stops responding for more than a specificed timeout,
// all running Services returned will race to re-acquire the lock and
// act as the new leader. Any pending iterators returned by List
// will fail, and clients of this storageapi.Service are encouraged
// to do their own retries on List operations.
//
// This Service also exposes pub/sub capabilities but message
// delivery is not guaranteed, and the same message could be
// dispatched multiple times, so protocols implemented on top
// should work around these limitations.
type Service struct {
	pubsub             *pubsub
	mu                 sync.Mutex
	open               StorageFactory
	svc                storageapi.Service
	lockFileListen     string // this distinction between listen/read is only used for tests
	lockFileRead       string
	lockFileRemoveSync string
	pid                string
	readyCtx           context.Context
	ready              func()
	readyErr           error

	cfg                  Config
	maxFollowFailures    int
	retryStrategy        retry.Strategy
	receiveRetryStrategy retry.Strategy
	connectRetryStrategy retry.Strategy

	closed      bool
	quitCh      chan struct{}
	closeWaitCh chan struct{}

	followFailures int
	subscriptions  map[string][][]byte
	// subscribed is every topic this peer subscribed to, tracked
	// independently of the pubsub client streams: those are dropped
	// the moment a leader connection breaks, so they cannot serve as
	// the record of what to restore on the next incarnation.
	subscribed map[string]struct{}
	active     storageapi.Service
}

const unixSocketPathMax = 103

// New allocates storage for a new Service and initializes it.
func New(open StorageFactory, lockFile string, cfg Config) *Service {
	ret := new(Service)
	ret.Init(open, lockFile, cfg)
	return ret
}

// Init initializes this Service to lead or follow, depending on whether
// lockFile has already been created or not.
//
// It is highly recommended to use DefaultConfig to build a sane Config.
func (s *Service) Init(open StorageFactory, lockFile string, cfg Config) {
	if cfg.Marshaler == nil {
		panic("empty Marshaler in config")
	}
	if open == nil {
		panic("nil StorageFactory")
	}

	lockFile = normalizedLockFile(lockFile)

	s.open = open
	if s.lockFileListen == "" {
		s.lockFileListen = lockFile
	}
	s.pid = strconv.Itoa(os.Getpid())
	s.lockFileRead = lockFile
	s.lockFileRemoveSync = lockFile + ".sync"
	s.subscriptions = make(map[string][][]byte)
	s.subscribed = make(map[string]struct{})
	s.pubsub = new(pubsub)
	s.pubsub.mu = &s.mu
	s.readyCtx, s.ready = context.WithCancel(context.Background())

	s.log(log.TraceLevel, "initializing new service peer...")

	s.cfg = cfg
	s.maxFollowFailures = int(cfg.TimeToCoup / (cfg.DialTimeout + cfg.ConnectRetryCadence))
	s.retryStrategy = retry.CombinedStrategy(
		retry.SequentialStrategy(cfg.MethodRetryCadence),
		retry.LimitStrategy(uint(methodRetryBudget(cfg)/cfg.MethodRetryCadence)),
	)
	s.receiveRetryStrategy = retry.SequentialStrategy(cfg.ReceiveRetryCadence)
	s.connectRetryStrategy = retry.SequentialStrategy(cfg.ConnectRetryCadence)

	s.quitCh = make(chan struct{})
	s.closeWaitCh = make(chan struct{})

	go debug.CapturePanicReport(func() {
		s.leadOrFollow()
	})
}

func normalizedLockFile(lockFile string) string {
	if len(lockFile)+len(".sync") <= unixSocketPathMax {
		return lockFile
	}
	sum := sha256.Sum256([]byte(lockFile))
	return filepath.Join("/tmp", "rune-fm-"+hex.EncodeToString(sum[:16]))
}

func (s *Service) waitReady(ctx context.Context) error {
	select {
	case <-s.readyCtx.Done():
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.readyErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Create satisfies storageapi.Service.
func (s *Service) Create(ctx context.Context, ID string, doc interface{}) error {
	if err := s.waitReady(ctx); err != nil {
		return err
	}
	return s.retryHandleDocErrs(ctx, func(ctx context.Context) (bool, error) {
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		err := active.Create(ctx, ID, doc)
		return s.isRetriableError(ctx, err), err
	})
}

// Set satisfies storageapi.Service.
func (s *Service) Set(ctx context.Context, ID string, doc interface{}) error {
	if err := s.waitReady(ctx); err != nil {
		return err
	}
	return s.retryHandleDocErrs(ctx, func(ctx context.Context) (bool, error) {
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		err := active.Set(ctx, ID, doc)
		return s.isRetriableError(ctx, err), err
	})
}

// Update satisfies storageapi.Service.
func (s *Service) Update(
	ctx context.Context, ID string, updates []storageapi.Update,
	preconds ...storageapi.Precondition,
) error {
	if err := s.waitReady(ctx); err != nil {
		return err
	}
	return s.retryHandleDocErrs(ctx, func(ctx context.Context) (bool, error) {
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		err := active.Update(ctx, ID, updates, preconds...)
		return s.isRetriableError(ctx, err), err
	})
}

// Get satisfies storageapi.Service.
func (s *Service) Get(ctx context.Context, ID string, doc interface{}) error {
	if err := s.waitReady(ctx); err != nil {
		return err
	}
	return s.retryHandleDocErrs(ctx, func(ctx context.Context) (bool, error) {
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		err := active.Get(ctx, ID, doc)
		return s.isRetriableError(ctx, err), err
	})
}

// Delete satisfies storageapi.Service.
func (s *Service) Delete(ctx context.Context, ID string) error {
	if err := s.waitReady(ctx); err != nil {
		return err
	}
	return s.retryHandleDocErrs(ctx, func(ctx context.Context) (bool, error) {
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		err := active.Delete(ctx, ID)
		return s.isRetriableError(ctx, err), err
	})
}

// Drop satisfies storageapi.DroppableService.
func (s *Service) Drop(ctx context.Context) error {
	if err := s.waitReady(ctx); err != nil {
		return err
	}
	return s.retryHandleDocErrs(ctx, func(ctx context.Context) (bool, error) {
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		droppable, ok := active.(storageapi.DroppableService)
		if !ok {
			return false, errors.New(
				"firstmover: active service does not support dropping")
		}
		err := droppable.Drop(ctx)
		return s.isRetriableError(ctx, err), err
	})
}

// ApplyBatch satisfies storageapi.BatchWriter.
func (s *Service) ApplyBatch(
	ctx context.Context, ops []storageapi.BatchOp,
) (results []storageapi.BatchOpResult, err error) {
	if err := s.waitReady(ctx); err != nil {
		return nil, err
	}
	err = s.retryHandleDocErrs(ctx, func(ctx context.Context) (bool, error) {
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		writer, ok := active.(storageapi.BatchWriter)
		if !ok {
			return false, errors.New(
				"firstmover: active service does not support batching")
		}
		results, err = writer.ApplyBatch(ctx, ops)
		return s.isRetriableError(ctx, err), err
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// List satisfies storageapi.Service.
func (s *Service) List(ctx context.Context, filters []storageapi.Filter) (
	it storageapi.Iterator, err error,
) {
	if err = s.waitReady(ctx); err != nil {
		return nil, err
	}
	// ignore the retry context here as the context semantics
	// are different for List: it's the iterator's of the subscription
	// rather than the call to List.
	err = s.retryHandleDocErrs(ctx, func(_ context.Context) (bool, error) {
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		it, err = active.List(ctx, filters)
		return s.isRetriableError(ctx, err), err
	})
	return
}

// Partition returns a partitioned peer service backed by this Service.
func (s *Service) Partition(name string) (storageapi.Service, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("firstmover: Partition on closed Service")
	}
	s.mu.Unlock()
	return &partitionService{root: s, chain: []string{name}}, nil
}

// Publish publishes an arbitrary message to the given topic.
// It will be received by all subscribers of this topic.
//
// This method returns ErrMessageTooLarge if a message is larger
// than MaxMessageSize.
func (s *Service) Publish(
	ctx context.Context, topic string, msg []byte,
) error {
	if len(msg) > s.cfg.MaxMessageSize {
		return ErrMessageTooLarge
	}
	if err := s.waitReady(ctx); err != nil {
		return err
	}
	return s.retryHandleDocErrs(ctx, func(ctx context.Context) (bool, error) {
		err := s.pubsub.publish(ctx, topic, msg)
		return s.isRetriableError(ctx, err), err
	})
}

// Subscribe creates a subscription created to the given topic,
// ensuring that future messages published are buffered for the next calls
// to Receive.
func (s *Service) Subscribe(
	ctx context.Context, topic string,
) error {
	if err := s.waitReady(ctx); err != nil {
		return err
	}
	var i int
	// ignore the retry context here as the context semantics
	// are different for subscribe: it's the context of the subscription
	// rather than the call to subscribe.
	return s.retryHandleDocErrs(ctx, func(_ context.Context) (bool, error) {
		_, _, err := s.pubsub.subscribe(ctx, topic, true)
		if i > 0 && err == errAlreadySubscribed {
			return false, nil
		}
		i++
		if err == nil {
			s.trackSubscription(topic)
		}
		return s.isRetriableError(ctx, err), err
	})
}

// Receive returns the next message published to the given topic,
// or blocks until a message is available.
//
// Under the hood a subscription is created so messages
// between calls to Receive are never lost. Clients that
// want fine-grained control over the lifecycle of the
// subscription should use Subscribe first and pass
// a context that can be canceled to cancel the subscription.
func (s *Service) Receive(
	ctx context.Context, topic string,
) (data []byte, err error) {
	if err = s.waitReady(ctx); err != nil {
		return nil, err
	}
	err = s.retryHandleDocErrsWithStrategy(ctx, func(ctx context.Context) (bool, error) {
		s.mu.Lock()
		pending := s.subscriptions[topic]
		if len(pending) > 0 {
			data = pending[0]
			s.subscriptions[topic] = pending[1:]
			s.mu.Unlock()
			return false, nil
		}
		s.mu.Unlock()
		data, err = s.pubsub.receive(ctx, topic)
		if err == nil {
			s.trackSubscription(topic)
		}
		return s.isRetriableError(ctx, err), err
	}, s.receiveRetryStrategy)
	return
}

// Close closes all resources associated with this Service.
func (s *Service) Close() (ret error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.log(log.TraceLevel, "Close called on peer...")
	s.closed = true
	if s.active != nil && s.svc != nil && s.active != s.svc {
		close(s.quitCh)
		// it's possible that Close on a follower was called after
		// we purposely shutdown connection due to leader also closing.
		_ = s.active.Close()
	} else if s.active != nil && s.active == s.svc { // leader
		s.pubsub.ready() // make sure that if we're not ready yet, we fail immediately
		s.pubsub.pubReady()
		s.mu.Unlock()
		// best effort, use server method directly so we guarantee delivery
		req := pubsubpb.PublishRequest{Topic: internalTopic, Data: internalMessageBye}
		if !testHookSuppressBye.Load() {
			_, _ = s.pubsub.Publish(context.Background(), &req)
		}
		s.mu.Lock()
		close(s.quitCh)
	} else {
		close(s.quitCh)
	}

	if err := s.pubsub.Close(); err != nil {
		ret = multierror.Append(ret, err)
	}
	s.mu.Unlock()

	<-s.closeWaitCh
	return
}

// IsLeader returns whether this instance is the leader of the system.
func (s *Service) IsLeader() bool {
	if err := s.waitReady(context.Background()); err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active != nil && s.svc == s.active
}

const (
	internalTopic            = "__pubsubinternal"
	internalMessageByeString = "BYE"
)

var (
	internalMessageBye = []byte(internalMessageByeString)
)

func (s *Service) follow(ctx context.Context, addr net.Addr) (bool, error) {
	quitCh := s.quitCh
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallSendMsgSize(s.cfg.MaxMessageSize),
			grpc.MaxCallRecvMsgSize(s.cfg.MaxMessageSize),
		),
	}
	opts = append(opts, storagerpc.NoSyncDialOptions()...)
	opts = append(opts,
		grpc.WithBlock(), //nolint:staticcheck // WithBlock is needed for dial-timeout behavior
		grpc.WithContextDialer(
			func(ctx context.Context, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, addr.Network(), addr.String())
			},
		))
	// do not override context as we're using it to know when Service is closing
	dialCtx, cancel := context.WithTimeout(ctx, s.cfg.DialTimeout)
	conn, err := grpc.DialContext(dialCtx, "", opts...) //nolint:staticcheck // NewClient doesn't support blocking dial with timeout
	cancel()
	if err != nil {
		// any Dial errors should always be retried. Any socket specific errors
		// that are not expected, and therefore would trigger a full halt will
		// be handled by the leader erro handling logic.
		return true, err
	}

	defer func() { _ = conn.Close() }()

	client := new(storagerpcclient.Client)
	client.Init(conn, s.cfg.Marshaler)

	// wait until connection is ready to unlock API mutex
loop:
	for {
		state := conn.GetState()
		switch state {
		case connectivity.Ready:
			s.mu.Lock()
			// we are holding the lock, so Close cannot race
			// to close quitCh; if closed, then we should return
			// immediately or else we leak resources.
			// if Close is waiting to acquire the lock,
			// then active will be set and the call to active.Close
			// will trigger the connection checking below to return.
			select {
			case <-quitCh:
				_ = client.Close()
				s.mu.Unlock()
				return false, nil
			default:
			}
			s.followFailures = 0 // reset
			s.pubsub.init(s.lockFileListen, s.pid)
			s.pubsub.initFollower(conn)
			// set active svc and unlock API
			s.setActiveAndUnlock(client)
			subscriptions := s.takeSubscriptions()
			s.mu.Unlock()
			s.resubscribe(ctx, subscriptions)
			s.monitorLeader(ctx, conn)
			s.log(log.DebugLevel, "Successfully connected to leader. Unlocking API...")
			break loop
		case connectivity.Idle:
			conn.Connect()
			fallthrough
		case connectivity.Connecting:
			didChange := conn.WaitForStateChange(ctx, state)
			if !didChange {
				s.log(log.TraceLevel, "Stopped monitoring for state changes. ctx is canceled")
				return false, nil
			}
		case connectivity.TransientFailure:
			failureCtx, cancelFn := context.WithTimeout(ctx, s.cfg.TransientFailureRecoverTimeout)
			didChange := conn.WaitForStateChange(failureCtx, connectivity.TransientFailure)
			cancelFn()
			if !didChange {
				return true, errors.New("timed out waiting for transient failure to recover")
			}
		case connectivity.Shutdown:
			return true, errors.New("grpc connection state = shutdown")
		default:
			panic(fmt.Sprintf("unknown connection state: %v", state))
		}
	}

	// monitor connection and quit if it exits
	defer s.recoverSubscriptions()
	for {
		state := conn.GetState()
		s.log(log.TraceLevel, "Monitoring for state changes. Current: %s", state)
		switch state {
		case connectivity.Idle:
			// A lost leader can leave gRPC idle; without a new RPC it will
			// never reconnect and cannot trigger the failure timeout below.
			conn.Connect()
			fallthrough
		case connectivity.Ready, connectivity.Connecting:
			if !conn.WaitForStateChange(ctx, state) {
				s.log(log.TraceLevel, "Stopped monitoring for state changes. ctx is canceled")
				return false, nil
			}
		case connectivity.TransientFailure:
			s.log(log.WarnLevel, "connection state is TransientFailure. Waiting for recover with timeout %s",
				s.cfg.TransientFailureRecoverTimeout)
			failureCtx, cancelFn := context.WithTimeout(ctx, s.cfg.TransientFailureRecoverTimeout)
			didChange := conn.WaitForStateChange(failureCtx, connectivity.TransientFailure)
			cancelFn()
			if !didChange {
				s.log(log.ErrorLevel, "failed to recover from TransientFailure. Reassessing lead/follow position")
				return true, errors.New("timed out waiting for transient failure to recover")
			}
		case connectivity.Shutdown:
			s.log(log.DebugLevel, "connection state is Shutdown")
			return true, errors.New("grpc connection state = shutdown")
		default:
			panic(fmt.Sprintf("unknown connection state: %v", state))
		}
	}
}

func (s *Service) resubscribe(ctx context.Context, subscriptions map[string][][]byte) {
	s.log(log.DebugLevel, "resubscribe: resubscribing to %d subscriptions", len(subscriptions))
	for topic, buffered := range subscriptions {
		_, stream, err := s.pubsub.subscribe(ctx, topic, false)
		if err != nil {
			s.log(log.WarnLevel, "resubscribe to %q: %v", topic, err)
			continue
		}
		s.log(log.DebugLevel, "resubscribe: created stream %p for topic %q, "+
			"sending %d messages", stream, topic, len(buffered))
		for i := range len(buffered) {
			stream <- msgError{msg: &pubsubpb.ReceiveMessage_Data{Data: buffered[i]}}
		}
		s.log(log.DebugLevel, "resubscribe: re-published %d messages from topic %q",
			len(buffered), topic)
	}
}

func (s *Service) monitorLeader(ctx context.Context, conn *grpc.ClientConn) {
	if _, _, err := s.pubsub.subscribe(ctx, internalTopic, false); err != nil {
		s.log(log.WarnLevel, "monitor leader: subscribe to internal bookkeeping topic: %v", err)
		return
	}

	go debug.CapturePanicReport(func() {

		for {
			data, err := s.pubsub.receive(ctx, internalTopic)
			if err != nil {
				// connection to leader died for expected or unexpected
				// reasons that are hard to determine from here. Do not log.
				return
			}
			if string(data) == internalMessageByeString {
				s.recoverSubscriptions()
				err := conn.Close()
				if err != nil {
					s.log(log.WarnLevel, "force close connection to leader: %v", err)
				}
			} else {
				s.log(log.WarnLevel, "received unknown message from internal topic: %v",
					string(data))
			}
		}

	})
}

func (s *Service) setActiveAndUnlock(svc storageapi.Service) {
	s.ready()
	s.active = svc
}

// recoverSubscriptions moves this follower's subscribed topics and
// their buffered messages out of the pubsub instance and into
// s.subscriptions, so the next incarnation can restore them: the
// instance is reset — dropping every client stream — as soon as this
// peer re-connects or takes over as leader.
//
// It must run on every way a follower can lose its leader, not just
// on the goodbye handshake: a leader that crashes, or whose goodbye
// races the connection teardown, would otherwise leave the follower
// silently unsubscribed from topics it still believes it is watching.
func (s *Service) recoverSubscriptions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for topic, entry := range s.pubsub.clientStreams {
		if topic == internalTopic {
			// the goodbye channel belongs to the connection that
			// just died; monitorLeader opens a fresh one per leader.
			continue
		}
		var msgs [][]byte
		// entry.ch is nil (len 0) while the subscription is still
		// being established: nothing is buffered yet, but the topic
		// must still be restored.
		for len(entry.ch) > 0 {
			msg := <-entry.ch
			if msg.err != nil {
				// the pump reports the dead connection into the
				// stream; that is not a message to replay.
				continue
			}
			msgs = append(msgs, msg.msg.GetData())
		}
		s.subscriptions[topic] = append(s.subscriptions[topic], msgs...)
	}
	s.log(log.DebugLevel, "leader lost, recovered %+v", s.subscriptions)
}

func (s *Service) trackSubscription(topic string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribed[topic] = struct{}{}
}

// takeSubscriptions hands the caller everything the next incarnation
// must restore: every topic this peer subscribed to, along with the
// messages buffered for topics whose stream was drained during the
// handoff. It must be called with s.mu held.
func (s *Service) takeSubscriptions() map[string][][]byte {
	subscriptions := s.subscriptions
	s.subscriptions = make(map[string][][]byte)
	for topic := range s.subscribed {
		if _, ok := subscriptions[topic]; !ok {
			subscriptions[topic] = nil
		}
	}
	return subscriptions
}

func (s *Service) lead(ctx context.Context, listener net.Listener) (reconnect bool, err error) {
	defer func() { _ = listener.Close() }()

	// Materialize the backing storage only now that this peer has
	// won the unix-socket lock. Backends that take exclusive OS-level
	// resources (bbolt's flock) rely on this contract so that
	// followers never race the leader for the file.
	svc, err := s.open()
	if err != nil {
		return true, fmt.Errorf("firstmover: open leader storage: %w", err)
	}
	defer func() {
		s.mu.Lock()
		toClose := s.svc
		s.svc = nil
		s.mu.Unlock()
		if toClose != nil {
			_ = toClose.Close()
		}
	}()

	s.mu.Lock()
	s.svc = svc
	server := storagerpc.NewServer(schemedoc.SyncWithLocker(svc, &s.mu), s.cfg.Marshaler)
	gsrv := grpc.NewServer(
		grpc.MaxSendMsgSize(s.cfg.MaxMessageSize),
		grpc.MaxRecvMsgSize(s.cfg.MaxMessageSize),
	)
	s.pubsub.init(s.lockFileListen, s.pid)
	s.mu.Unlock()
	// Release the partition handles the server opened lazily per
	// follower-requested path; otherwise each path a follower touched
	// pins a leader-side backend handle (a bbolt refcount) past
	// teardown, leaking on every leadership cycle. Registered before
	// gsrv.Stop's defer so that, under LIFO, the gRPC server stops
	// first and cannot repopulate the cache concurrently.
	defer func() { _ = server.Close() }()
	defer gsrv.Stop()

	docpb.RegisterDocumentStoreServer(gsrv, server)
	pubsubpb.RegisterPubSubServer(gsrv, s.pubsub)

	done := make(chan error)
	ready := make(chan struct{})
	quitCh := s.quitCh
	go debug.CapturePanicReport(func() {

		select {
		case done <- gsrv.Serve(&unlockListener{ready: ready, root: listener}):
		case <-quitCh:
		}

	})

	// wait for grpcserver to be listening
	// before we initialize pubsub as leader
	select {
	case <-ready:
	case <-ctx.Done():
		return true, ctx.Err()
	}

	s.mu.Lock()
	if err := s.pubsub.initLeader(ctx, gsrv, listener); err != nil {
		s.mu.Unlock()
		return false, fmt.Errorf("set pubsub leader: %w", err)
	}

	// set active svc and unlock API
	s.log(log.DebugLevel, "Successfully assumed position of leader. Unlocking API...")
	s.setActiveAndUnlock(svc)
	subscriptions := s.takeSubscriptions()
	s.mu.Unlock()

	// clean lock remove sync file, after a while to allow for reconnections
	// and protect the newly created leader from a coup.
	t := time.NewTimer(s.cfg.ConnectRetryCadence * 2)
	defer t.Stop()

	if hook := testHookLeadBeforeResubscribe.Load(); hook != nil {
		(*hook)()
	}
	s.resubscribe(ctx, subscriptions)
	s.pubsub.pubReady()
	for {
		select {
		case <-t.C:
			_ = os.Remove(s.lockFileRemoveSync)
		case <-quitCh:
			return false, nil
		case err := <-done:
			return false, err
		}
	}
}

func (s *Service) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logd.KeyClass, "firstmover.Service").
		WithField("address", fmt.Sprintf("%p", s)).
		WithField("lock", s.lockFileListen).
		WithField("pid", s.pid).
		Logf(level, msg, args...)
}

func (s *Service) leadOrFollow() {
	defer close(s.closeWaitCh)
	quitCh := s.quitCh

	ctx, cancel := context.WithCancel(context.Background())
	go debug.CapturePanicReport(func() {

		defer cancel()
		<-quitCh

	})

	// Create the directory up front rather than on ENOENT: Windows reports a
	// missing parent directory from bind(2) as WSAENETDOWN.
	if err := os.MkdirAll(filepath.Dir(s.lockFileListen), 0766); err != nil {
		s.log(log.WarnLevel, "create lock dir: %v", err)
	}

	fn := func(ctx context.Context) (bool, error) {
		var cfg net.ListenConfig
		listener, err := cfg.Listen(ctx, "unix", s.lockFileListen)
		if err == nil {
			retry, err := s.lead(ctx, listener)
			if err == nil || retry {
				return retry, err
			}
			s.log(log.WarnLevel, "Unexpected lead error: %v", err)
			return true, err
		}

		// depending on whether the error is a bind error or other we need to
		// wrap syscall errors and their os counterparts
		if !isAddrInUse(err) && // address already in use
			!errors.Is(err, os.ErrExist) && // file already exists
			!errors.Is(err, os.ErrInvalid) { // socket already bound to an address
			s.log(log.WarnLevel, "Unexpected error while trying to "+
				"acquire lock %q: %v", s.lockFileListen, err)
			return false, err
		}

		s.log(log.TraceLevel, "Expected error while trying to acquire lock %q: "+
			"fallback to follow instead: %v", s.lockFileListen, err)

		addr := net.UnixAddr{Net: "unix", Name: s.lockFileRead}
		retry, err := s.follow(ctx, &addr)
		if err != nil {
			s.followFailures++
		}
		if s.followFailures >= s.maxFollowFailures {
			s.followFailures = 0
			// this could happen if leader crashes. Generally the unix socket
			// is removed when listener is closed gracefully.
			s.log(log.WarnLevel, "Unresponsive leader. "+
				"Starting coup to elect a new leader: original folow error: %v", err)

			// only allow one follower to remove socket, to avoid a nasty
			// race condition: two followers race to remove the unix socket,
			// one is faster and is able to create unix socket, only to get
			// the slower one to remove it, resulting in a split brain.
			f, oerr := os.OpenFile(s.lockFileRemoveSync, os.O_CREATE|os.O_EXCL, 0766)
			if oerr != nil {
				if !os.IsExist(oerr) {
					s.log(log.ErrorLevel, "Could not synchronize coup: open sync file: %v", oerr)
					return true, err
				}

				fi, serr := os.Stat(s.lockFileRemoveSync)
				if serr != nil {
					s.log(log.ErrorLevel, "Could not synchronize coup: stat sync file: %v", serr)
					return true, err
				}
				// NOTE: if follower is taking too long, maybe that follower crashed too
				lastCreated := fi.ModTime()
				if time.Since(lastCreated) < s.cfg.ConnectRetryCadence*4 {
					s.log(log.DebugLevel, "Some other process is removing the lock")
					return true, err
				}
				s.log(log.WarnLevel, "Some other process is taking too long removing the lock, removing sync file...")
				_ = os.Remove(s.lockFileRemoveSync)
				return true, err
			}
			s.log(log.InfoLevel, "Removing lock to allow a leader to be elected...")
			_ = f.Close()
			_ = os.Remove(s.lockFileRead)
			return true, err
		}
		if err == nil || retry {
			s.log(log.TraceLevel, "Service is closing or expected follow error (left %d retries): err=%v",
				s.maxFollowFailures-s.followFailures, err)
			return retry, err
		}
		s.log(log.WarnLevel, "Unexpected follow error: %v", err)
		return false, err
	}

	err := retry.Retry(ctx, s.connectRetryStrategy, func(ctx context.Context) (bool, error) {
		retry, err := fn(ctx)
		select {
		case <-quitCh:
			return false, nil
		default:
			return retry, err
		}
	})

	select {
	case <-quitCh:
	default:
		s.mu.Lock()
		s.readyErr = err
		s.mu.Unlock()
		s.ready()
		s.log(log.ErrorLevel, "Unexpectedly stopped retrying: %v", err)
	}
}

func (s *Service) isRetriableError(ctx context.Context, err error) bool {
	// for readibility's sake, do not coalesce all branches into a boolean value
	if err == nil || ctx.Err() != nil {
		return false
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	s.mu.Lock()
	isLeader := s.active != nil && s.svc == s.active
	s.mu.Unlock()
	if !isLeader && s.cfg.CloseError != nil &&
		strings.Contains(err.Error(), s.cfg.CloseError.Error()) {
		return true
	}

	stat := status.Convert(err)
	c := stat.Code()
	return strings.Contains(stat.Message(), "connection error") ||
		strings.Contains(stat.Message(), "EOF") ||
		strings.Contains(stat.Message(), "Unavailable") ||
		strings.Contains(stat.Message(), "Canceled") ||
		c == codes.Unavailable || c == codes.DeadlineExceeded || c == codes.Aborted ||
		c == codes.Canceled
}

// If the last error is a storage error, then return that rather than transient transport errors.
func (s *Service) retryHandleDocErrs(
	ctx context.Context, fn func(ctx context.Context) (bool, error),
) error {
	return s.retryHandleDocErrsWithStrategy(ctx, fn, s.retryStrategy)
}

func (s *Service) retryHandleDocErrsWithStrategy(
	ctx context.Context, fn func(ctx context.Context) (bool, error),
	retryStrategy retry.Strategy,
) error {
	var err error
	retryErr := retry.Retry(ctx, retryStrategy, func(ctx context.Context) (bool, error) {
		var shouldRetry bool
		shouldRetry, err = fn(ctx)
		if !shouldRetry {
			// return nil so retryErr is nil and we know that we need to
			// return original error
			return shouldRetry, nil
		}
		select {
		case <-s.quitCh:
			return false, err
		default:
		}
		return shouldRetry, err
	})
	if retryErr != nil {
		return retryErr
	}
	return err
}

var _ net.Listener = (*unlockListener)(nil)

type unlockListener struct {
	root        net.Listener
	ready       chan struct{}
	readyClosed atomic.Bool
}

// Accept signals that the underlying server is ready
func (u *unlockListener) Accept() (net.Conn, error) {
	if u.readyClosed.CompareAndSwap(false, true) {
		close(u.ready)
	}
	return u.root.Accept()
}

func (u *unlockListener) Close() error {
	return u.root.Close()
}

// Addr returns the listener's network address.
func (u *unlockListener) Addr() net.Addr {
	return u.root.Addr()

}
