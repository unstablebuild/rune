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
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	multierror "github.com/ernestrc/go-multierror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/document"
	"github.com/unstablebuild/blue/document/docmarshal"
	"github.com/unstablebuild/blue/document/docmarshal/docbson"
	"github.com/unstablebuild/blue/document/docmarshal/docjson"
	"github.com/unstablebuild/blue/document/docmarshal/doctoml"
	"github.com/unstablebuild/blue/document/doctest"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"unstable.build/rune/internal/localstorage/bluestore"
)

func TestDefaultConfiguration(t *testing.T) {
	cfg := DefaultConfig()
	maxFollowFailures := int(cfg.TimeToCoup / (cfg.DialTimeout + cfg.ConnectRetryCadence))
	assert.Greater(t, maxFollowFailures, 1)
}

// TestMethodRetryBudgetOutlastsCoup pins that a call which races the
// death of the leader can wait out the whole election. The follower
// only starts the coup after TimeToCoup of failed dials and then has
// to bind its own listener, so a retry budget of exactly TimeToCoup
// leaves nothing for that startup and surfaces the transport error to
// the caller.
func TestMethodRetryBudgetOutlastsCoup(t *testing.T) {
	for name, cfg := range map[string]Config{
		"default": DefaultConfig(),
		"test":    testConfig(),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Greater(t, methodRetryBudget(cfg), cfg.TimeToCoup)
		})
	}
}

func TestNormalizedLockFile(t *testing.T) {
	t.Run("keeps short path unchanged", func(t *testing.T) {
		lockFile := filepath.Join(t.TempDir(), ".lock")
		lockFile = "/tmp/rune-fm-short.lock"
		assert.Equal(t, lockFile, normalizedLockFile(lockFile))
	})

	t.Run("shortens overlong path deterministically", func(t *testing.T) {
		lockFile := filepath.Join(t.TempDir(), strings.Repeat("a", 120), ".lock")
		actual := normalizedLockFile(lockFile)
		assert.NotEqual(t, lockFile, actual)
		assert.LessOrEqual(t, len(actual)+len(".sync"), unixSocketPathMax)
		assert.Equal(t, actual, normalizedLockFile(lockFile))
		assert.True(t, strings.HasPrefix(actual, "/tmp/rune-fm-"))
	})
}

func TestServiceIntegration(t *testing.T) {
	for name, _marshaler := range map[string]docmarshal.Marshaler{
		"bson": docbson.Marshaler(),
		"json": docjson.Marshaler(),
		"toml": doctoml.Marshaler(),
	} {
		marshaler := _marshaler
		t.Run(name, func(t *testing.T) {
			t.Run("single instance assumes leader", func(t *testing.T) {
				doctest.TestDocumentService(t, func(t *testing.T) document.Service {
					lockFile := makeTempLockFile(t)
					cfg := testConfig()
					cfg.Marshaler = marshaler
					svc := bluestore.AdaptTo(document.NewInMemoryServiceWithMarshaler(marshaler))
					return bluestore.AdaptFrom(New(factoryFor(svc), lockFile, cfg))
				})
			})

			t.Run("two instances, seconds assumes follower", func(t *testing.T) {
				doctest.TestDocumentService(t, func(t *testing.T) document.Service {
					lockFile := makeTempLockFile(t)
					svc := bluestore.AdaptTo(document.NewInMemoryServiceWithMarshaler(marshaler))
					cfg := testConfig()
					cfg.Marshaler = marshaler
					leader := New(factoryFor(svc), lockFile, cfg)
					var temp testStruct
					err := bluestore.AdaptFrom(leader).Get(context.Background(), lockFile, &temp)
					require.Equal(t, document.ErrNotFound, err)
					follower := New(factoryFor(svc), lockFile, cfg)
					return bluestore.AdaptFrom(follower)
				})
			})
		})
	}

	t.Run("single instance with lock path on non-existent folder attempts to create directory structure", func(t *testing.T) {
		doctest.TestDocumentService(t, func(t *testing.T) document.Service {
			lockFile := makeTempLockFile(t)
			lockFileDir := filepath.Join(filepath.Dir(lockFile), "newDir", "otherDir", "moreDirs")
			lockFile = filepath.Join(lockFileDir, ".lock")
			cfg := testConfig()
			cfg.Marshaler = docbson.Marshaler()
			svc := bluestore.AdaptTo(document.NewInMemoryServiceWithMarshaler(cfg.Marshaler))
			return bluestore.AdaptFrom(New(factoryFor(svc), lockFile, cfg))
		})
	})

	t.Run("single instance with overlong unix socket path fails fast", func(t *testing.T) {
		lockRoot := t.TempDir()
		lockFile := filepath.Join(lockRoot, strings.Repeat("a", 120), ".lock")
		cfg := testConfig()
		svc := New(factoryFor(bluestore.AdaptTo(document.NewInMemoryService())), lockFile, cfg)
		defer func() {
			_ = svc.Close()
		}()

		require.NoError(t, svc.Set(context.Background(), "dragonballz", &testStruct{A: "1234"}))

		var out testStruct
		require.NoError(t, svc.Get(context.Background(), "dragonballz", &out))
		require.Equal(t, "1234", out.A)
	})

	t.Run("startup errors are returned to callers instead of blocking", func(t *testing.T) {
		parentFile, err := os.CreateTemp("", "rune-fm-parent-")
		require.NoError(t, err)
		require.NoError(t, parentFile.Close())
		t.Cleanup(func() {
			_ = os.Remove(parentFile.Name())
		})

		lockFile := filepath.Join(parentFile.Name(), "child.sock")
		cfg := testConfig()
		svc := New(factoryFor(bluestore.AdaptTo(document.NewInMemoryService())), lockFile, cfg)
		defer func() {
			_ = svc.Close()
		}()

		err = svc.Set(context.Background(), "dragonballz", &testStruct{A: "1234"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a directory")
	})

	t.Run("single instance preconditions (bson)", func(t *testing.T) {
		doctest.TestDocumentServicePreconditions(t, func(t *testing.T) document.Service {
			lockFile := makeTempLockFile(t)
			cfg := testConfig()
			cfg.Marshaler = docbson.Marshaler()
			svc := bluestore.AdaptTo(document.NewInMemoryServiceWithMarshaler(cfg.Marshaler))
			return bluestore.AdaptFrom(New(factoryFor(svc), lockFile, cfg))
		})
	})

	t.Run("single instance eventually assumes leader if leader is non-responsive (lock leaked)", func(t *testing.T) {
		doctest.TestDocumentService(t, func(t *testing.T) document.Service {
			f, err := os.CreateTemp("", "")
			require.NoError(t, err)
			require.NoError(t, f.Close())
			svc := bluestore.AdaptTo(document.NewInMemoryService())
			return bluestore.AdaptFrom(New(factoryFor(svc), f.Name(), testConfig()))
		})
	})

	t.Run("two instances, seconds assumes leader after leader dies", func(t *testing.T) {
		doctest.TestDocumentService(t, func(t *testing.T) document.Service {
			lockFile := makeTempLockFile(t)
			svc := bluestore.AdaptTo(document.NewInMemoryService())
			leader := New(factoryFor(svc), lockFile, testConfig())

			err := leader.Set(context.Background(), "random", &testStruct{A: "1234"})
			require.NoError(t, err)
			follower := New(factoryFor(svc), lockFile, testConfig())

			var temp testStruct
			err = follower.Get(context.Background(), "random", &temp)
			require.NoError(t, err)
			require.Equal(t, "1234", temp.A)
			_ = leader.Close()

			err = follower.Delete(context.Background(), "random")
			require.NoError(t, err)

			return bluestore.AdaptFrom(follower)
		})
	})

	t.Run("remove leader lock, second instance assumes leader after leader is unresponsive", func(t *testing.T) {
		doctest.TestDocumentService(t, func(t *testing.T) document.Service {
			lockFile := makeTempLockFile(t)
			svc := bluestore.AdaptTo(document.NewInMemoryService())
			leader := New(factoryFor(svc), lockFile, testConfig())
			err := leader.Set(context.Background(), "dragonballz", &testStruct{A: "1234"})
			require.NoError(t, err)
			follower := New(factoryFor(svc), lockFile, testConfig())

			var temp testStruct
			err = follower.Get(context.Background(), "dragonballz", &temp)
			require.NoError(t, err)
			require.Equal(t, "1234", temp.A)
			err = follower.Delete(context.Background(), "dragonballz")
			require.NoError(t, err)

			_ = os.Remove(lockFile)
			return bluestore.AdaptFrom(follower)
		})
	})

	t.Run("remove leader lock, other assumes leader after leader is unresponsive", func(t *testing.T) {
		doctest.TestDocumentService(t, func(t *testing.T) document.Service {
			lockFile := makeTempLockFile(t)
			svc := bluestore.AdaptTo(document.NewInMemoryService())
			leader := New(factoryFor(svc), lockFile, testConfig())
			err := leader.Set(context.Background(), "dragonballz", &testStruct{A: "1234"})
			require.NoError(t, err)
			follower1 := New(factoryFor(svc), lockFile, testConfig())
			follower2 := New(factoryFor(svc), lockFile, testConfig())
			follower3 := New(factoryFor(svc), lockFile, testConfig())
			follower4 := New(factoryFor(svc), lockFile, testConfig())
			follower5 := New(factoryFor(svc), lockFile, testConfig())
			followers := []document.Service{
				bluestore.AdaptFrom(follower1),
				bluestore.AdaptFrom(follower2),
				bluestore.AdaptFrom(follower3),
				bluestore.AdaptFrom(follower4),
				bluestore.AdaptFrom(follower5),
			}

			for _, follower := range followers {
				var temp testStruct
				err = follower.Get(context.Background(), "dragonballz", &temp)
				require.NoError(t, err)
				require.Equal(t, "1234", temp.A)
			}
			err = leader.Delete(context.Background(), "dragonballz")
			require.NoError(t, err)

			_ = os.Remove(lockFile)
			return &alternatingService{svc: followers}
		})
	})

	t.Run("multiple instances", func(t *testing.T) {
		doctest.TestDocumentServiceNoList(t, func(t *testing.T) document.Service {
			const n = 8
			cfg := testConfig()

			lockFile := makeTempLockFile(t)
			svc := bluestore.AdaptTo(document.NewInMemoryService())

			peers := make([]*Service, 0, n)
			for range n {
				peer := New(factoryFor(svc), lockFile, cfg)
				_ = peer.Get(context.Background(), lockFile, nil)
				peers = append(peers, peer)
			}
			// The peer under test is never killed: the suite must
			// observe a service that survives its leaders dying, not
			// one that was closed under it.
			ret, churn := peers[n-1], peers[:n-1]

			killLeader := func() {
				for _, peer := range churn {
					if peer.IsLeader() {
						_ = peer.Close()
						return
					}
				}
			}
			// Each suite case is over in milliseconds, so start it in
			// the middle of a failover: its first calls must ride out
			// the re-election. The ticker keeps the churn going for
			// the longer cases.
			killLeader()
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				ticker := time.NewTicker(cfg.DialTimeout + cfg.ConnectRetryCadence)
				defer ticker.Stop()
				for {
					select {
					case <-stop:
						return
					case <-ticker.C:
					}
					killLeader()
				}
			}()
			// Every peer must be gone before the next subtest starts:
			// leaked swarms keep dialing and couping on their own lock
			// files and starve the peers of the subtests that follow.
			t.Cleanup(func() {
				close(stop)
				<-done
				cleanupNodes(t, churn...)
			})
			return bluestore.AdaptFrom(ret)
		})
	})

	t.Run("Close on massive network of peers", func(t *testing.T) {
		const n = 100
		cfg := testConfig()

		lockFile := makeTempLockFile(t)
		svc := bluestore.AdaptTo(document.NewInMemoryService())

		instances := make([]*Service, 0, n)
		for i := 0; i < n; i++ {
			instance := New(factoryFor(svc), lockFile, cfg)
			_ = instance.Get(context.Background(), lockFile, nil)
			instances = append(instances, instance)
		}
		for i := 0; i < n; i++ {
			instance := instances[i]
			require.NoError(t, instance.Close())
		}
	})
}

func TestCustomRetryableErrors(t *testing.T) {
	myError := errors.New("dia de los muertos")
	tsuite := []struct {
		desc         string
		methodError  error
		closeError   error
		wantSuccess  bool
		makeInstance func(storageapi.Service, string, Config) (storageapi.Service, func())
	}{
		{"leader does not retry error passed in Config", myError, myError, false, makeLeader},
		{"follower retries close error passed in Config until success", myError, myError, true, makeFollower},
		{"follower does not retry any error other than the error passed in Config", errors.New("wasup"), myError, false, makeFollower},
		{"follower does not retry any error if error in config is nil", errors.New("wasup"), nil, false, makeFollower},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			lockFile := makeTempLockFile(t)
			require.Error(t, tcase.methodError)
			mock := newTestService(tcase.methodError)
			cfg := testConfig()
			cfg.CloseError = tcase.closeError
			svc, doneFn := tcase.makeInstance(mock, lockFile, cfg)
			defer doneFn()
			err := svc.Set(context.Background(), "bluegrass", &testStruct{A: "1234"})
			if !tcase.wantSuccess {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestSetSucceedsWhenBackendIsSlowerThanMethodRetryCadence guards
// against the regression where firstmover wrapped every storage call
// in context.WithTimeout(ctx, MethodRetryCadence), so a backend that
// took longer than the cadence to write produced a chain of
// DeadlineExceeded retries against itself and ultimately failed —
// even when the caller passed context.Background(). The expected
// behavior is: the caller's deadline governs how long an attempt
// may take; firstmover only re-attempts on actual transport errors
// (Unavailable, leader CloseError, connection reset, …), not on a
// timeout that was caused by the backend simply being slow.
func TestSetSucceedsWhenBackendIsSlowerThanMethodRetryCadence(t *testing.T) {
	lockFile := makeTempLockFile(t)
	cfg := testConfig() // MethodRetryCadence = 20ms
	mock := &slowSetService{
		svc:   storagestub.NewInMemoryService(),
		delay: 10 * cfg.MethodRetryCadence,
	}
	leader := New(factoryFor(mock), lockFile, cfg)
	t.Cleanup(func() { _ = leader.Close() })

	require.NoError(t, leader.Set(context.Background(),
		"slow-but-not-broken", &testStruct{A: "ok"}))

	assert.Equal(t, int64(1), mock.calls.Load(),
		"slow Set must complete in a single attempt; "+
			"firstmover must not retry on its own self-imposed timeout")

	var out testStruct
	require.NoError(t, leader.Get(context.Background(),
		"slow-but-not-broken", &out))
	assert.Equal(t, "ok", out.A)
}

// TestPartitionDoesNotSpawnPeerGoroutines guards that Partition
// returns a thin wrapper rather than spawning a fresh firstmover
// peer per partition. Per-partition peers would each open their own
// unix-socket lockfile, run their own leadOrFollow goroutine, and
// subscribe over gRPC; the wrapper instead routes through the root.
func TestPartitionDoesNotSpawnPeerGoroutines(t *testing.T) {
	lockFile := makeTempLockFile(t)
	cfg := testConfig()
	leader := New(factoryFor(storagestub.NewInMemoryService()), lockFile, cfg)
	t.Cleanup(func() { _ = leader.Close() })

	_, err := leader.Partition("a")
	require.NoError(t, err)
	b, err := leader.Partition("b")
	require.NoError(t, err)
	_, err = b.Partition("child")
	require.NoError(t, err)

	// Per-partition lockfiles would be siblings of the root lockfile
	// with the partition name appended (the pre-refactor scheme).
	// Any such file means a child firstmover spawned its own peer.
	entries, err := filepath.Glob(lockFile + ".*")
	require.NoError(t, err)
	for _, e := range entries {
		// .sync is the leader's removal-sync file, owned by the root.
		if strings.HasSuffix(e, ".sync") {
			continue
		}
		t.Fatalf("Partition spawned its own lockfile peer: %s", e)
	}

	// Partition on a closed root must fail rather than silently
	// hand out a wrapper that will never route to a live backend.
	require.NoError(t, leader.Close())
	_, err = leader.Partition("after-close")
	assert.Error(t, err)
}

// TestOpenStorageFactoryInvokedOnlyByLeader guards the leader-only
// storage invariant: a peer that does not win the unix-socket lock
// must never invoke its StorageFactory, because backends like bbolt
// hold exclusive OS-level resources that only the leader is allowed
// to acquire. Deriving a partition must not re-invoke the factory
// either; the wrapper routes through the root's existing backend.
func TestOpenStorageFactoryInvokedOnlyByLeader(t *testing.T) {
	lockFile := makeTempLockFile(t)
	cfg := testConfig()
	shared := storagestub.NewInMemoryService()
	var opens atomic.Int32
	factory := func() (storageapi.Service, error) {
		opens.Add(1)
		return noCloseService{Service: shared}, nil
	}

	leader := New(factory, lockFile, cfg)
	defer func() { _ = leader.Close() }()
	require.NoError(t, leader.Set(context.Background(), "k", &testStruct{A: "v"}))
	require.Equal(t, int32(1), opens.Load())

	part, err := leader.Partition("x")
	require.NoError(t, err)
	require.NoError(t, part.Set(context.Background(), "k", &testStruct{A: "vx"}))
	require.Equal(t, int32(1), opens.Load(),
		"Partition must not re-invoke the storage factory")

	follower := New(factory, lockFile, cfg)
	defer func() { _ = follower.Close() }()
	var got testStruct
	require.NoError(t, follower.Get(context.Background(), "k", &got))
	assert.Equal(t, "v", got.A)
	assert.Equal(t, int32(1), opens.Load(),
		"follower must not invoke the storage factory")
}

// TestPartitionRoutesThroughRootLeader verifies that a partition
// wrapper on a follower reads values that the leader wrote via its
// own partition wrapper, exercising the gRPC wire path's partition
// chain handling.
func TestPartitionRoutesThroughRootLeader(t *testing.T) {
	lockFile := makeTempLockFile(t)
	cfg := testConfig()
	shared := storagestub.NewInMemoryService()
	factory := factoryFor(shared)

	leader := New(factory, lockFile, cfg)
	defer func() { _ = leader.Close() }()
	leaderPart, err := leader.Partition("p")
	require.NoError(t, err)
	require.NoError(t, leaderPart.Set(context.Background(),
		"k", &testStruct{A: "via-leader"}))

	follower := New(factory, lockFile, cfg)
	defer func() { _ = follower.Close() }()
	followerPart, err := follower.Partition("p")
	require.NoError(t, err)
	var got testStruct
	require.NoError(t, followerPart.Get(context.Background(), "k", &got))
	assert.Equal(t, "via-leader", got.A)
}

// TestServiceTakesOverStaleLock covers a leader that died without cleaning up
// (the socket file is left behind with nobody listening) and a data directory
// that does not exist yet: either way the next process must become leader.
func TestServiceTakesOverStaleLock(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lockFile func(t *testing.T) string
	}{
		{"socket left by a dead leader", func(t *testing.T) string {
			lockFile := makeTempLockFile(t)
			l, err := net.Listen("unix", lockFile)
			require.NoError(t, err)
			l.(*net.UnixListener).SetUnlinkOnClose(false)
			require.NoError(t, l.Close())
			_, err = os.Stat(lockFile)
			require.NoError(t, err, "stale socket must remain on disk")
			return lockFile
		}},
		{"lock directory does not exist yet", func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "missing", "db.lock")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(factoryFor(storagestub.NewInMemoryService()), tc.lockFile(t), testConfig())
			t.Cleanup(func() { _ = svc.Close() })

			part, err := svc.Partition("p")
			require.NoError(t, err)
			require.NoError(t, part.Set(context.Background(), "k", &testStruct{A: "v"}))
			assert.True(t, svc.IsLeader())
		})
	}
}

func TestPartitionWorksAfterLeaderFailoverWithoutGoodbye(t *testing.T) {
	testHookSuppressBye.Store(true)
	t.Cleanup(func() { testHookSuppressBye.Store(false) })
	testPartitionWorksAfterLeaderFailover(t)
}

// TestPartitionWorksAfterLeaderFailover verifies that values written
// to a partition on the original leader remain readable from the
// same partition name after the follower takes leadership.
func TestPartitionWorksAfterLeaderFailover(t *testing.T) {
	testPartitionWorksAfterLeaderFailover(t)
}

func testPartitionWorksAfterLeaderFailover(t *testing.T) {
	t.Helper()
	lockFile := makeTempLockFile(t)
	cfg := testConfig()
	shared := storagestub.NewInMemoryService()
	factory := factoryFor(shared)

	leader := New(factory, lockFile, cfg)
	t.Cleanup(func() { _ = leader.Close() })
	leaderPart, err := leader.Partition("p")
	require.NoError(t, err)
	require.NoError(t, leaderPart.Set(context.Background(),
		"k", &testStruct{A: "pre-coup"}))

	follower := New(factory, lockFile, cfg)
	defer func() { _ = follower.Close() }()
	// Touch follower so it joins the cluster before the coup.
	followerPart, err := follower.Partition("p")
	require.NoError(t, err)
	var got testStruct
	require.NoError(t, followerPart.Get(context.Background(), "k", &got))
	require.Equal(t, "pre-coup", got.A)

	require.NoError(t, leader.Close())
	require.Eventually(t, func() bool {
		return follower.IsLeader()
	}, 5*time.Second, 10*time.Millisecond,
		"follower must take leadership after leader closes")

	require.NoError(t, followerPart.Get(context.Background(), "k", &got))
	assert.Equal(t, "pre-coup", got.A,
		"partition data must survive leader failover")
}

// TestPartitionNestingIsolation verifies that nested partitions are
// keyed by their full path: root.Partition("a").Partition("b") must
// not collide with root.Partition("b").
func TestPartitionNestingIsolation(t *testing.T) {
	lockFile := makeTempLockFile(t)
	cfg := testConfig()
	leader := New(factoryFor(storagestub.NewInMemoryService()), lockFile, cfg)
	defer func() { _ = leader.Close() }()

	a, err := leader.Partition("a")
	require.NoError(t, err)
	ab, err := a.Partition("b")
	require.NoError(t, err)
	b, err := leader.Partition("b")
	require.NoError(t, err)

	require.NoError(t, ab.Set(context.Background(), "k", &testStruct{A: "nested"}))
	require.NoError(t, b.Set(context.Background(), "k", &testStruct{A: "sibling"}))

	var got testStruct
	require.NoError(t, ab.Get(context.Background(), "k", &got))
	assert.Equal(t, "nested", got.A)
	require.NoError(t, b.Get(context.Background(), "k", &got))
	assert.Equal(t, "sibling", got.A)
}

// TestPartitionListIteratorSurvivesLeaderLoss exercises the leader-
// loss-during-iterator hazard: a partition List on the leader returns
// an iterator backed by the leader's local store; if leadership flips
// before the caller drains, reads and the final Close must not panic
// or wedge, and the walked partition refs must not leak past the
// leader's own backend teardown.
func TestPartitionListIteratorSurvivesLeaderLoss(t *testing.T) {
	lockFile := makeTempLockFile(t)
	cfg := testConfig()
	tracked := &partitionCloseTracker{Service: storagestub.NewInMemoryService()}
	leader := New(factoryFor(tracked), lockFile, cfg)
	t.Cleanup(func() { _ = leader.Close() })

	part, err := leader.Partition("p")
	require.NoError(t, err)
	for i := range 3 {
		require.NoError(t, part.Set(context.Background(),
			"k"+strconv.Itoa(i), &testStruct{A: "v" + strconv.Itoa(i)}))
	}

	it, err := part.List(context.Background(), nil)
	require.NoError(t, err)

	require.NoError(t, leader.Close())

	// Reads after leader loss may succeed (iterator was fully buffered
	// in storagestub) or fail (closing backend interrupted the read).
	// Either is acceptable: what we care about is no panic, no
	// deadlock, and a clean Close.
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for it.HasNext() {
			var doc testStruct
			_ = it.NextTo(&doc)
		}
	}()
	select {
	case <-drainDone:
	case <-time.After(2 * time.Second):
		t.Fatal("iterator drain wedged after leader loss")
	}
	require.NoError(t, it.Close())

	// The partitioned view caches one resolved handle across its
	// Set/List ops; closing the view releases it. Without this, the
	// cached leader-side handle would still be live.
	require.NoError(t, part.Close())

	closed := tracked.partitionsClosed.Load()
	opened := tracked.partitionsOpened.Load()
	assert.Equalf(t, opened, closed,
		"every walked partition must be closed exactly once "+
			"(opened=%d closed=%d) — iterator Close after leader loss "+
			"must release walked partition refs", opened, closed)
}

// partitionCloseTracker wraps a storageapi.Service to count partition
// open/close events, mirroring the bbolt-style refcount semantics
// where every Partition() must be balanced by a Close().
type partitionCloseTracker struct {
	storageapi.Service
	partitionsOpened atomic.Int32
	partitionsClosed atomic.Int32
}

func (t *partitionCloseTracker) Partition(name string) (storageapi.Service, error) {
	inner, err := t.Service.Partition(name)
	if err != nil {
		return nil, err
	}
	t.partitionsOpened.Add(1)
	return &trackedPartition{Service: inner, parent: t}, nil
}

func (t *partitionCloseTracker) Close() error { return nil }

type trackedPartition struct {
	storageapi.Service
	parent *partitionCloseTracker
	closed atomic.Bool
}

func (t *trackedPartition) Close() error {
	if t.closed.CompareAndSwap(false, true) {
		t.parent.partitionsClosed.Add(1)
	}
	return t.Service.Close()
}

func (t *trackedPartition) Partition(name string) (storageapi.Service, error) {
	return t.parent.Partition(name)
}

// TestOpenStorageReopenedOnCoup guards that a peer promoted to
// leader after the previous leader exits invokes its StorageFactory
// again. Backends like bbolt re-acquire the OS flock here, so a
// missing reopen would either deadlock the new leader or leave it
// without backing storage.
func TestOpenStorageReopenedOnCoup(t *testing.T) {
	lockFile := makeTempLockFile(t)
	cfg := testConfig()
	shared := storagestub.NewInMemoryService()
	var opens atomic.Int32
	factory := func() (storageapi.Service, error) {
		opens.Add(1)
		return noCloseService{Service: shared}, nil
	}

	leader := New(factory, lockFile, cfg)
	require.NoError(t, leader.Set(context.Background(), "k", &testStruct{A: "v"}))
	require.Equal(t, int32(1), opens.Load())

	follower := New(factory, lockFile, cfg)
	defer func() { _ = follower.Close() }()
	var got testStruct
	require.NoError(t, follower.Get(context.Background(), "k", &got))

	require.NoError(t, leader.Close())

	require.Eventually(t, func() bool {
		return follower.IsLeader()
	}, 5*time.Second, 10*time.Millisecond,
		"follower must eventually take leadership after leader closes")

	require.NoError(t, follower.Set(context.Background(),
		"k2", &testStruct{A: "after-coup"}))
	require.NoError(t, follower.Get(context.Background(), "k2", &got))
	assert.Equal(t, "after-coup", got.A)
	assert.Equal(t, int32(2), opens.Load(),
		"factory must be re-invoked when a new leader is elected")
}

// slowSetService delegates Set to an in-memory backend after a
// configurable delay. All other methods are passthroughs.
type slowSetService struct {
	svc   storageapi.Service
	delay time.Duration
	calls atomic.Int64
}

func (s *slowSetService) Set(ctx context.Context, ID string, doc any) error {
	s.calls.Add(1)
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.svc.Set(ctx, ID, doc)
}

func (s *slowSetService) Get(ctx context.Context, ID string, doc any) error {
	return s.svc.Get(ctx, ID, doc)
}

func (s *slowSetService) Create(ctx context.Context, ID string, doc any) error {
	return s.svc.Create(ctx, ID, doc)
}

func (s *slowSetService) Update(
	ctx context.Context, ID string, updates []storageapi.Update,
	preconds ...storageapi.Precondition,
) error {
	return s.svc.Update(ctx, ID, updates, preconds...)
}

func (s *slowSetService) Delete(ctx context.Context, ID string) error {
	return s.svc.Delete(ctx, ID)
}

func (s *slowSetService) List(
	ctx context.Context, filters []storageapi.Filter,
) (storageapi.Iterator, error) {
	return s.svc.List(ctx, filters)
}

func (s *slowSetService) Partition(name string) (storageapi.Service, error) {
	partitioned, err := s.svc.Partition(name)
	if err != nil {
		return nil, err
	}
	return &slowSetService{svc: partitioned, delay: s.delay}, nil
}

func (s *slowSetService) Close() error {
	return s.svc.Close()
}

func makeFollower(svc storageapi.Service, lockFile string, cfg Config) (storageapi.Service, func()) {
	var temp testStruct
	leader := New(factoryFor(svc), lockFile, cfg)
	_ = leader.Get(context.Background(), "bla", &temp)
	follower := New(factoryFor(svc), lockFile, cfg)
	_ = follower.Get(context.Background(), "bla", &temp)
	return follower, func() {
		_ = leader.Close()
		_ = follower.Close()
	}
}

func makeLeader(svc storageapi.Service, lockFile string, cfg Config) (storageapi.Service, func()) {
	leader := New(factoryFor(svc), lockFile, cfg)
	return leader, func() {
		_ = leader.Close()
	}
}

// factoryFor returns a StorageFactory that wraps an existing
// storageapi.Service so leadership rotations across peers sharing
// a single in-memory backing store do not close the shared store
// when a leader releases the lock.
func factoryFor(svc storageapi.Service) StorageFactory {
	return func() (storageapi.Service, error) {
		return noCloseService{Service: svc}, nil
	}
}

// noCloseService swallows Close so tests can hand the same
// storageapi.Service to multiple peers via separate factory
// invocations. The leader closes the value the factory returned
// when leadership ends; the underlying shared store outlives it.
type noCloseService struct {
	storageapi.Service
}

func (n noCloseService) Close() error { return nil }

func (n noCloseService) Partition(name string) (storageapi.Service, error) {
	return n.Service.Partition(name)
}

type testStruct struct {
	A string
}

func testConfig() Config {
	return Config{
		Marshaler:                      docbson.Marshaler(),
		TransientFailureRecoverTimeout: 450 * time.Millisecond,
		MethodRetryCadence:             20 * time.Millisecond,
		ReceiveRetryCadence:            500 * time.Millisecond,
		ConnectRetryCadence:            50 * time.Millisecond,
		TimeToCoup:                     500 * time.Millisecond,
		DialTimeout:                    50 * time.Millisecond,
		MaxMessageSize:                 DefaultMaxMessageSize * 2,
	}
}

type testService struct {
	err   error
	tries int
	svc   storageapi.Service
}

func newTestService(err error) storageapi.Service {
	return &testService{err: err, svc: storagestub.NewInMemoryService()}
}

func (t *testService) Set(ctx context.Context, ID string, doc any) error {
	if t.err == nil {
		panic("incorrect test case")
	}
	t.tries++
	if t.tries < 2 {
		return t.err
	}
	return t.svc.Set(ctx, ID, doc)
}

func (t *testService) Get(ctx context.Context, ID string, doc any) error {
	return t.svc.Get(ctx, ID, doc)
}

func (t *testService) Create(ctx context.Context, ID string, doc any) error {
	panic("unimplemented")
}

func (t *testService) Update(
	ctx context.Context, ID string, updates []storageapi.Update,
	preconds ...storageapi.Precondition,
) error {
	panic("unimplemented")
}

func (t *testService) Delete(ctx context.Context, ID string) error {
	panic("unimplemented")
}

func (t *testService) List(ctx context.Context, filters []storageapi.Filter) (storageapi.Iterator, error) {
	panic("unimplemented")
}

func (t *testService) Partition(name string) (storageapi.Service, error) {
	partitioned, err := t.svc.Partition(name)
	if err != nil {
		return nil, err
	}
	return &testService{err: t.err, svc: partitioned}, nil
}

func (t *testService) Close() error {
	return nil
}

func makeTempLockFile(t *testing.T) string {
	f, err := os.CreateTemp("", "")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, os.Remove(f.Name()))
	t.Cleanup(func() {
		_ = os.Remove(f.Name())
	})
	return f.Name()
}

type alternatingService struct {
	i   atomic.Int64
	svc []document.Service
}

func (a *alternatingService) Create(ctx context.Context, ID string, doc interface{}) error {
	i := int(a.i.Add(1))
	return a.svc[i%len(a.svc)].Create(ctx, ID, doc)
}

func (a *alternatingService) Set(ctx context.Context, ID string, doc interface{}) error {
	i := int(a.i.Add(1))
	return a.svc[i%len(a.svc)].Set(ctx, ID, doc)
}

func (a *alternatingService) Update(
	ctx context.Context, ID string, updates []document.Update,
	precond ...document.Precondition,
) error {
	i := int(a.i.Add(1))
	return a.svc[i%len(a.svc)].Update(ctx, ID, updates, precond...)
}

func (a *alternatingService) Get(ctx context.Context, ID string, doc interface{}) error {
	i := int(a.i.Add(1))
	return a.svc[i%len(a.svc)].Get(ctx, ID, doc)
}

func (a *alternatingService) Delete(ctx context.Context, ID string) error {
	i := int(a.i.Add(1))
	return a.svc[i%len(a.svc)].Delete(ctx, ID)
}

func (a *alternatingService) List(ctx context.Context, filters []document.Filter) (document.Iterator, error) {
	i := int(a.i.Add(1))
	return a.svc[i%len(a.svc)].List(ctx, filters)
}

func (a *alternatingService) Close() (ret error) {
	for _, svc := range a.svc {
		ret = multierror.Append(ret, svc.Close())
	}
	return
}
