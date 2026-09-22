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

package textrpc

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi/browserrpc"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi/textrpc"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	gomock "go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	tbrowserrpc "unstable.build/rune/internal/browser/browserrpc"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/term/sh"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/workspace"
)

func TestClientServerIntegration(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///test")
	require.NoError(t, err)
	t.Run("client through server calls underlying editor Edit", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, nopLocker{})

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		expectEdit(t, ed, uri, "hero", false, false)
		buf := cell.NewBuffer()
		buf.WriteString("hero")

		_, err := client.Edit(uri, buf, false, false)
		require.NoError(t, err)
	})

	t.Run("client passes readOnly and recover params", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, nopLocker{})

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		expectEdit(t, ed, uri, "hero", true, true)
		buf := cell.NewBuffer()
		buf.WriteString("hero")
		_, err := client.Edit(uri, buf, true, true)
		require.NoError(t, err)
	})

	t.Run("client through server calls underlying Editor", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, nopLocker{})

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		ed.EXPECT().Editor(gomock.Any()).Times(1).
			DoAndReturn(func(_uri workspaceapi.URI) (tui.Handler, error) {
				assert.Equal(t, _uri, uri)
				return handler.NewTestHandler(), nil
			})
		_, err := client.Editor(uri)
		require.NoError(t, err)
	})

	t.Run("underlying editor Edito errors bubble up to client", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, nopLocker{})

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		ed.EXPECT().Edit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, errors.New("The Upsetter")).
			Times(1)

		_, err := client.Edit(uri, cell.NewBuffer(), false, false)
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "The Upsetter"))
	})

	t.Run("client through server calls underlying editor Subscribe", func(t *testing.T) {
		str1 := "Granola Lola"
		tsuite := []struct {
			name       string
			evType     textapi.EventType
			trigger    func(t *testing.T, resourceName string, ed text.Editor, buf *cell.Buffer)
			start, end *term.Coordinates
			content    *string
		}{
			{
				"Edit->EventTypeOpen",
				textapi.EventTypeOpen,
				func(t *testing.T, resourceName string, ed text.Editor, buf *cell.Buffer) {
					ed.Edit(context.Background(), uri, buf, false, false)
				}, nil, nil, nil,
			},
			{
				"Edit->EventTypeEdit",
				textapi.EventTypeEdit,
				func(t *testing.T, resourceName string, ed text.Editor, buf *cell.Buffer) {
					ed.Edit(context.Background(), uri, buf, false, false)
					buf.WriteString(str1)
				}, &term.Coordinates{}, &term.Coordinates{}, &str1,
			},
			{
				"Edit->EventTypeEdit",
				textapi.EventTypeEdit,
				func(t *testing.T, resourceName string, ed text.Editor, buf *cell.Buffer) {
					buf.WriteString(str1)
					ed.Edit(context.Background(), uri, buf, false, false)
					buf.DeleteRow(0)
				}, &term.Coordinates{}, &term.Coordinates{Y: 1}, nil,
			},
			{
				"Handle->EventTypeCursor",
				textapi.EventTypeCursor,
				func(t *testing.T, resourceName string, ed text.Editor, buf *cell.Buffer) {
					buf.WriteString(str1)
					h, err := ed.Edit(context.Background(), uri, buf, false, false)
					assert.NoError(t, err)
					h.Handle(term.Event{Ch: 'l'})
				}, &term.Coordinates{}, &term.Coordinates{}, nil,
			},
			{
				"Handle->EventTypeSelection",
				textapi.EventTypeSelection,
				func(t *testing.T, resourceName string, ed text.Editor, buf *cell.Buffer) {
					buf.WriteString(str1)
					h, err := ed.Edit(context.Background(), uri, buf, false, false)
					assert.NoError(t, err)
					h.Handle(term.Event{Ch: 'v'})
				}, &term.Coordinates{}, &term.Coordinates{}, nil,
			},
		}

		for i, _tcase := range tsuite {
			tcase := _tcase
			t.Run(tcase.name, func(t *testing.T) {
				var wg sync.WaitGroup
				ed := texttest.NopEditorWithCallback(wg.Done)
				s := NewServer(nopNotifications{}, ed, new(sync.Mutex))

				client, closeFn := setupIntTest(t, s)
				defer closeFn()

				wg.Add(1)
				err := client.SubscribeEvents([]textapi.EventType{tcase.evType},
					text.FuncEventHandler(func(ctx context.Context, ev textapi.Event) bool {
						defer wg.Done()
						if tcase.start != nil {
							assert.Equal(t, *tcase.start, ev.Start)
						}
						if tcase.end != nil {
							assert.Equal(t, *tcase.end, ev.End)
						}
						if tcase.content != nil {
							assert.Equal(t, *tcase.content, ev.Content)
						}
						return false
					}))
				require.NoError(t, err)

				// wait for subscribe callback
				wg.Wait()

				// proceed to trigger
				wg.Add(1)

				buf := cell.NewBuffer()
				tcase.trigger(t, strconv.Itoa(i), ed, buf)
				wg.Wait()

				s.editor.Lock()
				defer s.editor.Unlock()

				assert.NoError(t, s.Close())
			})
		}
	})

	t.Run("calls unsubscribe if event stream completes", func(t *testing.T) {
		var wg sync.WaitGroup
		ed := texttest.NopEditorWithCallback(wg.Done)
		s := NewServer(nopNotifications{}, ed, new(sync.Mutex))

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		wg.Add(1)
		err := client.SubscribeEvents([]textapi.EventType{textapi.EventTypeOpen},
			text.FuncEventHandler(func(ctx context.Context, ev textapi.Event) bool {
				defer wg.Done()
				return true
			}))
		require.NoError(t, err)

		// wait for subscribe callback
		wg.Wait()

		// proceed to trigger
		wg.Add(1)
		ed.Edit(context.Background(), uri, cell.NewBuffer(), false, false)

		// wg panics if Done called but not added
		wg.Wait()

		// close
		s.editor.Lock()
		defer s.editor.Unlock()

		assert.NoError(t, s.Close())
	})

	t.Run("event handler drops messages if event handler server is not processing events", func(t *testing.T) {
		var wg sync.WaitGroup
		ed := texttest.NopEditorWithCallback(wg.Done)
		s := NewServer(nopNotifications{}, ed, new(sync.Mutex))

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		var mu sync.Mutex
		evs := make(map[textapi.EventType]textapi.Event)

		wg.Add(1)
		err := client.SubscribeEvents([]textapi.EventType{
			textapi.EventTypeOpen,
			textapi.EventTypeClose,
			textapi.EventTypeFlush,
			textapi.EventTypeEdit,
			textapi.EventTypeScroll,
			textapi.EventTypeFocus,
			textapi.EventTypeUnfocus,
			textapi.EventTypeCursor,
			textapi.EventTypeSelection,
		}, text.FuncEventHandler(func(ctx context.Context, ev textapi.Event) bool {
			mu.Lock()
			defer mu.Unlock()
			evs[ev.Type] = ev
			return false
		}))
		require.NoError(t, err)

		// wait for subscribe callback, which also uses wg
		wg.Wait()

		subs := ed.Subscribers()
		require.Len(t, subs, 9)
		require.Len(t, subs[textapi.EventTypeOpen], 1)
		handler := subs[textapi.EventTypeOpen][0]

		// proceed to trigger, should not deadlock
		mu.Lock() // try to deadlock
		for i := range 10000 {
			tpe := textapi.EventType(i % 9)
			handler.Handle(context.Background(), textapi.Event{URI: uri, Type: tpe})
		}

		mu.Unlock()
		// there's no deterministic way to know how many msgs will
		// be buffered by the underlying transport, so this is the only way
		time.Sleep(5 * time.Second)
		mu.Lock()

		require.Len(t, evs, 9)
		s.editor.Lock()
		defer s.editor.Unlock()

		assert.NoError(t, s.Close())
	})

	t.Run("SetLocationList sets the location list of the remote editor", func(t *testing.T) {
		var wg sync.WaitGroup
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, new(sync.Mutex))

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		expectEdit(t, ed, uri, "", false, false)
		h, err := client.Edit(uri, cell.NewBuffer(), false, false)
		require.NoError(t, err)

		l := text.LocationSlice([]textapi.Location{loc2})

		mock := expectEditor(t, ctrl, ed, uri)
		mock.EXPECT().SetLocationList(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(pri textapi.LocationPriority, id string, ll text.LocationList) {
				defer wg.Done()
				assertLocation(t, ll, 0, loc2)
				assert.Equal(t, locID, id)
				assertLocationListLen(t, ll, 1)
				assert.Equal(t, textapi.LocationPriorityError, pri)
			}).Times(1)

		wg.Add(1)
		err = client.SetLocationList(h, textapi.LocationPriorityError, locID, l)
		require.NoError(t, err)

		wg.Wait()
	})

	t.Run("SetDefaultAttributes sets the default attrs of the remote editor", func(t *testing.T) {
		var wg sync.WaitGroup
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, new(sync.Mutex))

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		expectEdit(t, ed, uri, "", false, false)
		h, err := client.Edit(uri, cell.NewBuffer(), false, false)
		require.NoError(t, err)

		expectedAttrs := term.Attributes{
			Attrs: term.AttrUnderline | term.AttrBold,
			Fg:    term.ColorWhite,
			Bg:    term.ColorNavy,
		}

		mock := expectEditor(t, ctrl, ed, uri)
		mock.EXPECT().SetDefaultAttributes(gomock.Any()).
			DoAndReturn(func(attrs term.Attributes) {
				defer wg.Done()
				assert.Equal(t, expectedAttrs, attrs)
			}).Times(1)

		wg.Add(1)
		err = client.SetDefaultAttributes(h, expectedAttrs)
		require.NoError(t, err)

		wg.Wait()
	})

	t.Run("Writer returns a Writer that is able to modify underlying buffer", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, nopLocker{})

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		buf := cell.NewBuffer()
		expectEdit(t, ed, uri, "", false, false)
		h, err := client.Edit(uri, buf, false, false)
		require.NoError(t, err)

		w := client.CellEditor(h)
		at := term.Coordinates{X: 1}

		mock := expectEditor(t, ctrl, ed, uri)
		mock.EXPECT().CellEditor().Return(buf.Editor()).Times(1)
		from, to, _, err := w.Edit(context.Background(), at, at, "el\nAridio")

		require.NoError(t, err)
		require.Equal(t, term.Coordinates{}, from)
		require.Equal(t, term.Coordinates{X: 6, Y: 1}, to)
		require.Equal(t, " el\nAridio", buf.String())

		mock.EXPECT().CellEditor().Return(buf.Editor()).Times(1)
		start, end, str, err := w.Edit(
			context.Background(), term.Coordinates{}, term.Coordinates{Y: 1}, "")

		require.NoError(t, err)
		assert.Equal(t, term.Coordinates{}, start)
		assert.Equal(t, term.Coordinates{}, end)
		assert.Equal(t, " el\n", str)
		assert.Equal(t, "Aridio", buf.String())
	})

	t.Run("Reader returns a Reader that is able to read underlying buffer", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, nopLocker{})

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		buf := cell.NewBuffer()
		buf.WriteString("guacamole")
		expectEdit(t, ed, uri, "guacamole", false, false)
		h, err := client.Edit(uri, buf, false, false)
		require.NoError(t, err)

		mock := expectEditor(t, ctrl, ed, uri)
		mock.EXPECT().CellView().Return(buf.View()).Times(2)

		r := client.CellView(h)
		cells, err := r.RawCells()
		require.NoError(t, err)
		assert.Equal(t, "guacamole", term.CellsToString(cells))

		buf.WriteString("\npollos hermanos")

		cells, err = r.RawCells()
		require.NoError(t, err)
		assert.Equal(t, "guacamole\npollos hermanos", term.CellsToString(cells))
	})

	t.Run("dispatches commands to subscribed command handler", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, new(sync.Mutex))
		var wg sync.WaitGroup

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		var (
			dispatched       textapi.Command
			subscribed       textapi.CommandManual
			subscribedTimes  int
			subscribedClient text.CommandHandler
		)
		handler := textapi.FuncCommandHandler(func(_ context.Context, man textapi.Command) error {
			dispatched = man
			wg.Done()
			return nil
		}, nil)

		man := textapi.CommandManual{
			Name:     "bla",
			Synopsis: "blabla",
			Commands: []textapi.CommandManual{
				{
					Name:     "ble",
					Synopsis: "bleble",
				},
			},
		}
		ed.EXPECT().UnsubscribeCommand(gomock.Any()).Return(text.ErrCommandNotRegistered)
		ed.EXPECT().SubscribeCommand(gomock.Any(), gomock.Any()).DoAndReturn(
			func(man textapi.CommandManual, h text.CommandHandler) error {
				subscribed = man
				subscribedTimes++
				subscribedClient = h
				return nil
			})
		err := client.SubscribeCommand(man, handler)
		require.NoError(t, err)

		require.Equal(t, 1, subscribedTimes)
		assert.Equal(t, "bla", subscribed.Name)
		assert.Equal(t, "blabla", subscribed.Synopsis)
		require.Len(t, subscribed.Commands, 1)
		assert.Equal(t, "ble", subscribed.Commands[0].Name)
		assert.Equal(t, "bleble", subscribed.Commands[0].Synopsis)
		require.NotNil(t, subscribedClient)

		// dispatch
		resource := texttest.NewTestHandler()
		resource.URI = uri
		command := textapi.Command{
			Name:     "bla",
			Args:     []string{"ble"},
			URI:      uri,
			Resource: resource,
			Window:   browserrpc.NewWindow(199),
		}
		command.Cursor.Content = term.Coordinates{X: 1, Y: 2}
		command.Cursor.Window = term.Coordinates{X: 3, Y: 4}
		s.editor.Lock()

		wg.Add(1)
		err = subscribedClient.HandleCommand(context.Background(), command)
		s.editor.Unlock()
		require.NoError(t, err)

		wg.Wait()
		assert.Equal(t, "bla", dispatched.Name)
		require.Len(t, dispatched.Args, 1)
		assert.Equal(t, "ble", dispatched.Args[0])
		assert.Equal(t, "file:///test", dispatched.URI.String())
		assert.Equal(t, uint64(199), dispatched.Window.WindowID())
		assert.Equal(t, "file:///test", dispatched.Resource.Resource().String())

		waitForReplaceCommand(t, &wg, ed, client, "bla")
	})

	t.Run("handles command handler error by notifying user", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		ed := texttest.NewMockEditor(ctrl)
		noti := browsertest.NewMockNotifications(ctrl)
		s := NewServer(noti, ed, new(sync.Mutex))

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		var (
			subscribedClient text.CommandHandler
			wg               sync.WaitGroup
		)
		handler := textapi.FuncCommandHandler(func(_ context.Context, man textapi.Command) error {
			return errors.New("boom")
		}, nil)

		man := textapi.CommandManual{Name: "bla"}
		ed.EXPECT().UnsubscribeCommand(gomock.Any()).Return(text.ErrCommandNotRegistered)
		ed.EXPECT().SubscribeCommand(gomock.Any(), gomock.Any()).DoAndReturn(
			func(man textapi.CommandManual, h text.CommandHandler) error {
				subscribedClient = h
				return nil
			})
		err := client.SubscribeCommand(man, handler)
		require.NoError(t, err)

		require.NotNil(t, subscribedClient)

		// dispatch
		resource := texttest.NewTestHandler()
		resource.URI = uri
		command := textapi.Command{Name: "bla"}
		wg.Add(1)
		noti.EXPECT().Notify(gomock.Any(), gomock.Any(), gomock.Any()).
			Times(1).
			DoAndReturn(func(browserapi.NotificationLevel, string, ...any) (string, error) {
				wg.Done()
				return "", nil
			})
		s.editor.Lock()
		err = subscribedClient.HandleCommand(context.Background(), command)
		s.editor.Unlock()
		require.NoError(t, err)

		wg.Wait() // notify was called
		waitForReplaceCommand(t, &wg, ed, client, "bla")
	})

	t.Run("returns subscribe error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, new(sync.Mutex))

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		handler := textapi.FuncCommandHandler(func(_ context.Context, man textapi.Command) error {
			return nil
		}, nil)

		man := textapi.CommandManual{Name: "bla"}
		ed.EXPECT().UnsubscribeCommand(gomock.Any()).Return(text.ErrCommandNotRegistered)
		ed.EXPECT().SubscribeCommand(gomock.Any(), gomock.Any()).DoAndReturn(
			func(man textapi.CommandManual, h text.CommandHandler) error {
				return errors.New("boom")
			})
		err := client.SubscribeCommand(man, handler)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})

	t.Run("completes commands", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, new(sync.Mutex))

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		var (
			subscribedClient text.CommandHandler
			dispatched       string
			dispatchedArgs   []string
			dispatchedTimes  int
		)
		expectedIterator := iterator.FromSlice([]string{"a", "b", "c"})
		handler := textapi.FuncCommandHandler(
			func(_ context.Context, man textapi.Command) error {
				return nil
			}, func(_ context.Context, cmd string, args []string) (
				iterator.Iterator[string], error,
			) {
				dispatched = cmd
				dispatchedArgs = args
				dispatchedTimes++
				return expectedIterator, nil
			})

		man := textapi.CommandManual{Name: "bla"}
		ed.EXPECT().UnsubscribeCommand(gomock.Any()).Return(text.ErrCommandNotRegistered)
		ed.EXPECT().SubscribeCommand(gomock.Any(), gomock.Any()).DoAndReturn(
			func(man textapi.CommandManual, h text.CommandHandler) error {
				subscribedClient = h
				return nil
			})
		err := client.SubscribeCommand(man, handler)
		require.NoError(t, err)

		require.NotNil(t, subscribedClient)

		// complete
		s.editor.Lock()
		it, _, err := subscribedClient.Complete(context.Background(), textapi.Command{Name: "bla", Args: []string{"ble"}})
		s.editor.Unlock()
		require.NoError(t, err)

		actualIterator, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Equal(t, actualIterator, []string{"a", "b", "c"})

		require.Equal(t, 1, dispatchedTimes)
		assert.Equal(t, "bla", dispatched)
		require.Len(t, dispatchedArgs, 1)
		assert.Equal(t, "ble", dispatchedArgs[0])

		var wg sync.WaitGroup
		waitForReplaceCommand(t, &wg, ed, client, "bla")
	})

	t.Run("returns complete error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, new(sync.Mutex))

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		var (
			subscribedClient text.CommandHandler
		)
		handler := textapi.FuncCommandHandler(
			func(_ context.Context, man textapi.Command) error {
				return nil
			}, func(_ context.Context, cmd string, args []string) (
				iterator.Iterator[string], error,
			) {
				return nil, errors.New("boom")
			})

		man := textapi.CommandManual{Name: "bla"}
		ed.EXPECT().UnsubscribeCommand(gomock.Any()).Return(text.ErrCommandNotRegistered)
		ed.EXPECT().SubscribeCommand(gomock.Any(), gomock.Any()).DoAndReturn(
			func(man textapi.CommandManual, h text.CommandHandler) error {
				subscribedClient = h
				return nil
			})
		err := client.SubscribeCommand(man, handler)
		require.NoError(t, err)

		require.NotNil(t, subscribedClient)

		s.editor.Lock()
		it, _, err := subscribedClient.Complete(context.Background(),
			textapi.Command{Name: "bla", Args: []string{"ble"}})
		s.editor.Unlock()
		require.NoError(t, err)

		_, err = iterator.ToSlice(context.Background(), it)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")

		var wg sync.WaitGroup
		waitForReplaceCommand(t, &wg, ed, client, "bla")
	})

	t.Run("returns iterator error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, new(sync.Mutex))

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		var (
			subscribedClient text.CommandHandler
		)
		handler := textapi.FuncCommandHandler(
			func(_ context.Context, man textapi.Command) error {
				return nil
			}, func(_ context.Context, cmd string, args []string) (
				iterator.Iterator[string], error,
			) {
				return iterator.Error[string](errors.New("boom")), nil
			})

		man := textapi.CommandManual{Name: "bla"}
		ed.EXPECT().UnsubscribeCommand(gomock.Any()).Return(text.ErrCommandNotRegistered)
		ed.EXPECT().SubscribeCommand(gomock.Any(), gomock.Any()).DoAndReturn(
			func(man textapi.CommandManual, h text.CommandHandler) error {
				subscribedClient = h
				return nil
			})
		err := client.SubscribeCommand(man, handler)
		require.NoError(t, err)

		require.NotNil(t, subscribedClient)

		s.editor.Lock()
		it, _, err := subscribedClient.Complete(context.Background(),
			textapi.Command{Name: "bla", Args: []string{"ble"}})
		s.editor.Unlock()
		require.NoError(t, err)

		_, err = iterator.ToSlice(context.Background(), it)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")

		var wg sync.WaitGroup
		waitForReplaceCommand(t, &wg, ed, client, "bla")
	})

	t.Run("dispatches repl commands to subscribed repl handler", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, new(sync.Mutex))

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		var (
			subscribed     textapi.CommandManual
			subscribedRepl textapi.REPLHandler
		)
		ed.EXPECT().UnregisterREPLCommand(gomock.Any()).Return(text.ErrCommandNotRegistered)
		ed.EXPECT().RegisterREPLCommand(gomock.Any(), gomock.Any()).DoAndReturn(
			func(man textapi.CommandManual, h textapi.REPLHandler) error {
				subscribed = man
				subscribedRepl = h
				return nil
			})

		var wg sync.WaitGroup
		handler := &testREPLHandler{
			handleFn: func(_ context.Context, cmd repl.Command, _ repl.ProgressWriter) (
				iterator.Iterator[component.Responsive], error,
			) {
				defer wg.Done()
				assert.Equal(t, "status", cmd.Name)
				assert.Equal(t, []string{"--all"}, cmd.Args)
				return iterator.FromSlice([]component.Responsive{
					component.NewResponsiveString("ok", component.StringResponsiveConfig{}),
				}), nil
			},
			completeFn: func(context.Context, string, []string) (iterator.Iterator[string], error) {
				return iterator.FromSlice[string](nil), nil
			},
			helpFn: func(context.Context, []string) (iterator.Iterator[component.Responsive], error) {
				return iterator.FromSlice[component.Responsive](nil), nil
			},
		}

		man := textapi.CommandManual{
			Name:     "status",
			Summary:  "show status",
			Synopsis: "status [--all]",
		}
		err := client.RegisterREPLCommand(man, handler)
		require.NoError(t, err)

		assert.Equal(t, man.Name, subscribed.Name)
		assert.Equal(t, man.Summary, subscribed.Summary)
		assert.Equal(t, man.Synopsis, subscribed.Synopsis)
		require.NotNil(t, subscribedRepl)

		wg.Add(1)
		it, err := subscribedRepl.HandleCommand(
			context.Background(),
			repl.Command{Name: "status", Args: []string{"--all"}},
			repl.NopProgressWriter(),
		)
		require.NoError(t, err)

		out, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		wg.Wait()
		require.Len(t, out, 1)
		assert.Equal(t, "ok", renderResponsiveString(out[0], 80))
	})

	// A dispatcher that takes the editor lock the moment the server releases
	// it after registering the command (xsandbox's expect_command +
	// invoke_command, or a keystroke landing on the event loop) must still
	// see the subscribe response on the wire before its own dispatch.
	t.Run("dispatch right after registration follows the subscribe response", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		ed := texttest.NewMockEditor(ctrl)
		lock := &dispatchOnUnlockLocker{}
		s := NewServer(nopNotifications{}, ed, lock)

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		var wg sync.WaitGroup
		handler := textapi.FuncCommandHandler(func(_ context.Context, cmd textapi.Command) error {
			defer wg.Done()
			assert.Equal(t, "bla", cmd.Name)
			return nil
		}, nil)

		ed.EXPECT().UnsubscribeCommand(gomock.Any()).Return(text.ErrCommandNotRegistered)
		ed.EXPECT().SubscribeCommand(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ textapi.CommandManual, h text.CommandHandler) error {
				lock.dispatch = func() {
					assert.NoError(t, h.HandleCommand(
						context.Background(), textapi.Command{Name: "bla"}))
				}
				return nil
			})
		// Stream teardown replaces the handler; on the failing path it runs
		// after the client has already given up on the stream.
		ed.EXPECT().UnsubscribeCommand(gomock.Any()).Return(nil).AnyTimes()
		ed.EXPECT().SubscribeCommand(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

		wg.Add(1)
		require.NoError(t, client.SubscribeCommand(textapi.CommandManual{Name: "bla"}, handler))
		wg.Wait()
	})

	t.Run("repl dispatch right after registration follows the subscribe response", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		ed := texttest.NewMockEditor(ctrl)
		lock := &dispatchOnUnlockLocker{}
		s := NewServer(nopNotifications{}, ed, lock)

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		var wg sync.WaitGroup
		handler := &testREPLHandler{
			handleFn: func(_ context.Context, cmd repl.Command, _ repl.ProgressWriter) (
				iterator.Iterator[component.Responsive], error,
			) {
				defer wg.Done()
				assert.Equal(t, "status", cmd.Name)
				return iterator.FromSlice[component.Responsive](nil), nil
			},
		}

		ed.EXPECT().UnregisterREPLCommand(gomock.Any()).Return(text.ErrCommandNotRegistered)
		ed.EXPECT().RegisterREPLCommand(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ textapi.CommandManual, h textapi.REPLHandler) error {
				lock.dispatch = func() {
					it, err := h.HandleCommand(context.Background(),
						repl.Command{Name: "status"}, repl.NopProgressWriter())
					if assert.NoError(t, err) {
						_, err = iterator.ToSlice(context.Background(), it)
						assert.NoError(t, err)
					}
				}
				return nil
			})

		wg.Add(1)
		require.NoError(t, client.RegisterREPLCommand(textapi.CommandManual{Name: "status"}, handler))
		wg.Wait()
	})

	t.Run("completes repl commands", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, new(sync.Mutex))

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		var subscribedRepl textapi.REPLHandler
		ed.EXPECT().UnregisterREPLCommand(gomock.Any()).Return(text.ErrCommandNotRegistered)
		ed.EXPECT().RegisterREPLCommand(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ textapi.CommandManual, h textapi.REPLHandler) error {
				subscribedRepl = h
				return nil
			})

		var wg sync.WaitGroup
		handler := &testREPLHandler{
			handleFn: func(context.Context, repl.Command, repl.ProgressWriter) (
				iterator.Iterator[component.Responsive], error,
			) {
				return iterator.FromSlice[component.Responsive](nil), nil
			},
			completeFn: func(_ context.Context, cmd string, args []string) (iterator.Iterator[string], error) {
				defer wg.Done()
				assert.Equal(t, "status", cmd)
				assert.Equal(t, []string{"-"}, args)
				return iterator.FromSlice([]string{"--all", "--json"}), nil
			},
			helpFn: func(context.Context, []string) (iterator.Iterator[component.Responsive], error) {
				return iterator.FromSlice[component.Responsive](nil), nil
			},
		}

		err := client.RegisterREPLCommand(textapi.CommandManual{Name: "status"}, handler)
		require.NoError(t, err)
		require.NotNil(t, subscribedRepl)

		wg.Add(1)
		it, err := subscribedRepl.Complete(context.Background(), "status", []string{"-"})
		require.NoError(t, err)
		out, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		wg.Wait()
		assert.Equal(t, []string{"--all", "--json"}, out)
	})

	t.Run("returns repl help output", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, new(sync.Mutex))

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		var subscribedRepl textapi.REPLHandler
		ed.EXPECT().UnregisterREPLCommand(gomock.Any()).Return(text.ErrCommandNotRegistered)
		ed.EXPECT().RegisterREPLCommand(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ textapi.CommandManual, h textapi.REPLHandler) error {
				subscribedRepl = h
				return nil
			})

		var wg sync.WaitGroup
		handler := &testREPLHandler{
			handleFn: func(context.Context, repl.Command, repl.ProgressWriter) (
				iterator.Iterator[component.Responsive], error,
			) {
				return iterator.FromSlice[component.Responsive](nil), nil
			},
			completeFn: func(context.Context, string, []string) (iterator.Iterator[string], error) {
				return iterator.FromSlice[string](nil), nil
			},
			helpFn: func(_ context.Context, args []string) (iterator.Iterator[component.Responsive], error) {
				defer wg.Done()
				assert.Equal(t, []string{"sub"}, args)
				return iterator.FromSlice([]component.Responsive{
					component.NewResponsiveString("usage: status sub", component.StringResponsiveConfig{}),
				}), nil
			},
		}

		err := client.RegisterREPLCommand(textapi.CommandManual{Name: "status"}, handler)
		require.NoError(t, err)
		require.NotNil(t, subscribedRepl)

		wg.Add(1)
		it, err := subscribedRepl.Help(context.Background(), []string{"sub"})
		require.NoError(t, err)
		out, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		wg.Wait()
		require.Len(t, out, 1)
		assert.Equal(t, "usage: status sub", renderResponsiveString(out[0], 80))
	})

	t.Run("returns repl registration error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, new(sync.Mutex))

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		ed.EXPECT().UnregisterREPLCommand(gomock.Any()).Return(text.ErrCommandNotRegistered)
		ed.EXPECT().RegisterREPLCommand(gomock.Any(), gomock.Any()).Return(errors.New("boom"))

		handler := &testREPLHandler{
			handleFn: func(context.Context, repl.Command, repl.ProgressWriter) (
				iterator.Iterator[component.Responsive], error,
			) {
				return iterator.FromSlice[component.Responsive](nil), nil
			},
			completeFn: func(context.Context, string, []string) (iterator.Iterator[string], error) {
				return iterator.FromSlice[string](nil), nil
			},
			helpFn: func(context.Context, []string) (iterator.Iterator[component.Responsive], error) {
				return iterator.FromSlice[component.Responsive](nil), nil
			},
		}

		err := client.RegisterREPLCommand(textapi.CommandManual{Name: "status"}, handler)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})

	t.Run("forwards handler progress to caller's ProgressWriter", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		ed := texttest.NewMockEditor(ctrl)
		s := NewServer(nopNotifications{}, ed, new(sync.Mutex))

		client, closeFn := setupIntTest(t, s)
		defer closeFn()

		var subscribedRepl textapi.REPLHandler
		ed.EXPECT().UnregisterREPLCommand(gomock.Any()).Return(text.ErrCommandNotRegistered)
		ed.EXPECT().RegisterREPLCommand(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ textapi.CommandManual, h textapi.REPLHandler) error {
				subscribedRepl = h
				return nil
			})

		handler := &testREPLHandler{
			handleFn: func(_ context.Context, _ repl.Command, pw repl.ProgressWriter) (
				iterator.Iterator[component.Responsive], error,
			) {
				pw.Progress(10, 100, "B")
				pw.Progress(50, 100, "B")
				pw.Progress(100, 100, "B")
				return iterator.FromSlice[component.Responsive](nil), nil
			},
			completeFn: func(context.Context, string, []string) (iterator.Iterator[string], error) {
				return iterator.FromSlice[string](nil), nil
			},
			helpFn: func(context.Context, []string) (iterator.Iterator[component.Responsive], error) {
				return iterator.FromSlice[component.Responsive](nil), nil
			},
		}

		err := client.RegisterREPLCommand(textapi.CommandManual{Name: "dl"}, handler)
		require.NoError(t, err)
		require.NotNil(t, subscribedRepl)

		pw := &recordingProgressWriter{}
		it, err := subscribedRepl.HandleCommand(
			context.Background(),
			repl.Command{Name: "dl"},
			pw,
		)
		require.NoError(t, err)
		_, err = iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)

		progresses := pw.get()
		require.GreaterOrEqual(t, len(progresses), 1,
			"expected at least one progress update to reach the caller's ProgressWriter")
		// The last update must be the terminal one; earlier samples may
		// be dropped by the non-blocking send on the server side.
		last := progresses[len(progresses)-1]
		assert.Equal(t, int64(100), last.progress)
		assert.Equal(t, int64(100), last.total)
		assert.Equal(t, "B", last.units)
	})
}

// TestClientServer_ShellProgressE2E wires up the full chain the editor
// uses when it dispatches a shell command to an out-of-process
// extension's REPL handler:
//
//	sh.commandHandler -> <registry> -> replCommandClientStream.HandleCommand
//	  -> gRPC -> replCommandServerStream -> testREPLHandler.handleFn
//
// It asserts that Progress updates emitted by the extension-side handler
// flow all the way back to the caller-supplied ProgressWriter.
func TestClientServer_ShellProgressE2E(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	ed := texttest.NewMockEditor(ctrl)
	s := NewServer(nopNotifications{}, ed, new(sync.Mutex))

	client, closeFn := setupIntTest(t, s)
	defer closeFn()

	// Capture the client-side stream adapter (a textapi.REPLHandler) that
	// the server installs when the extension calls RegisterREPLCommand.
	var subscribedRepl textapi.REPLHandler
	ed.EXPECT().UnregisterREPLCommand(gomock.Any()).Return(text.ErrCommandNotRegistered)
	ed.EXPECT().RegisterREPLCommand(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ textapi.CommandManual, h textapi.REPLHandler) error {
			subscribedRepl = h
			return nil
		})

	// Extension-side handler. Emits progress, then finishes cleanly.
	extHandler := &testREPLHandler{
		handleFn: func(
			_ context.Context, _ repl.Command, pw repl.ProgressWriter,
		) (iterator.Iterator[component.Responsive], error) {
			require.NotNil(t, pw)
			pw.Progress(0, 0, "B")
			pw.Progress(100, 100, "B")
			return iterator.FromSlice[component.Responsive](nil), nil
		},
		completeFn: func(context.Context, string, []string) (iterator.Iterator[string], error) {
			return iterator.FromSlice[string](nil), nil
		},
		helpFn: func(context.Context, []string) (iterator.Iterator[component.Responsive], error) {
			return iterator.FromSlice[component.Responsive](nil), nil
		},
	}

	err := client.RegisterREPLCommand(
		textapi.CommandManual{Name: "dl"}, extHandler,
	)
	require.NoError(t, err)
	require.NotNil(t, subscribedRepl)

	// Wrap the captured REPLHandler in a sh layer — this is the exact
	// flow the companion shell uses: repl.Handler -> sh -> registry -> REPL.
	shellCmd := sh.New(replByNameHandler{router: subscribedRepl}, workspaceapi.URI{})

	pw := &recordingProgressWriter{}
	ctx := context.Background()
	iter, err := shellCmd.HandleCommand(ctx, repl.Command{Name: "dl"}, pw)
	require.NoError(t, err)
	_, err = iterator.ToSlice(ctx, iter)
	require.NoError(t, err)

	samples := pw.get()
	require.NotEmpty(t, samples,
		"expected caller's ProgressWriter to receive updates end-to-end")
	last := samples[len(samples)-1]
	assert.Equal(t, int64(100), last.progress)
	assert.Equal(t, int64(100), last.total)
	assert.Equal(t, "B", last.units)
}

// replByNameHandler adapts a textapi.REPLHandler into a
// repl.CommandHandler by delegating HandleCommand/Complete directly. It
// models the lookup step done by a production command registry.
type replByNameHandler struct {
	router textapi.REPLHandler
}

// dispatchOnUnlockLocker runs dispatch, once, while still holding the lock
// on the first Unlock after it was armed: the earliest instant a competing
// dispatcher can observe the registered command.
type dispatchOnUnlockLocker struct {
	mu       sync.Mutex
	dispatch func()
}

func (l *dispatchOnUnlockLocker) Lock() { l.mu.Lock() }

func (l *dispatchOnUnlockLocker) Unlock() {
	if fn := l.dispatch; fn != nil {
		l.dispatch = nil
		fn()
	}
	l.mu.Unlock()
}

func (h replByNameHandler) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	return h.router.HandleCommand(ctx, cmd, pw)
}

func (h replByNameHandler) Complete(
	ctx context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	return h.router.Complete(ctx, cmd, args)
}

type progressSample struct {
	progress, total int64
	units           string
}

type recordingProgressWriter struct {
	mu      sync.Mutex
	samples []progressSample
}

func (w *recordingProgressWriter) Progress(progress, total int64, units string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.samples = append(w.samples, progressSample{progress, total, units})
}

func (w *recordingProgressWriter) get() []progressSample {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]progressSample(nil), w.samples...)
}

type testREPLHandler struct {
	handleFn   func(context.Context, repl.Command, repl.ProgressWriter) (iterator.Iterator[component.Responsive], error)
	completeFn func(context.Context, string, []string) (iterator.Iterator[string], error)
	helpFn     func(context.Context, []string) (iterator.Iterator[component.Responsive], error)
}

func (h *testREPLHandler) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	return h.handleFn(ctx, cmd, pw)
}

func (h *testREPLHandler) Complete(
	ctx context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	return h.completeFn(ctx, cmd, args)
}

func (h *testREPLHandler) Help(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	return h.helpFn(ctx, args)
}

func renderResponsiveString(r component.Responsive, width int) string {
	height := r.Height(width)
	w := term.NewStringWriter(width, height)
	r.Resize(width, height)
	r.Draw(w)
	_ = w.Flush()
	return strings.TrimSpace(w.String())
}

func waitForReplaceCommand(
	t *testing.T, wg *sync.WaitGroup,
	ed *texttest.MockEditor, client *Client, expectCmd string,
) {
	wg.Add(1)
	ed.EXPECT().UnsubscribeCommand(gomock.Any()).DoAndReturn(
		func(cmd string) error {
			assert.Equal(t, expectCmd, cmd)
			wg.Done()
			return nil
		})
	ed.EXPECT().SubscribeCommand(gomock.Any(), gomock.Any()).Return(nil)
	require.NoError(t, client.Close())
	wg.Wait()
}

func TestRPCTab(t *testing.T) {
	testTabIntegration(t, func(ed text.Editor, mu *sync.Mutex) (*text.Component, browserapi.WindowManager, error) {
		c, err := newTestComponentErr(ed)
		if err != nil {
			return nil, nil, err
		}

		s := tbrowserrpc.NewServer(c, mu)
		s.SetSyncMode()

		client, closeFn := setupWmIntTest(t, s)
		t.Cleanup(func() {
			mu.Lock()
			s.Stop()
			mu.Unlock()
			closeFn()
		})

		return c, client, err
	})
}

func assertLocation(t *testing.T, l text.LocationList, idx int, loca textapi.Location) {
	resetLocationList(l)
	var i int
	for loc, ok := l.Current(); ok; loc, ok = l.Next() {
		if idx == i {
			assert.Equal(t, loca, loc)
			return
		}
		i++
	}
}
func assertLocationListLen(t *testing.T, l text.LocationList, length int) {
	resetLocationList(l)
	var i int
	for _, ok := l.Current(); ok; _, ok = l.Next() {
		i++
	}
	assert.Equal(t, length, i)
}

func resetLocationList(l text.LocationList) {
	for {
		_, ok := l.Prev()
		if !ok {
			break
		}
	}
}

func testTabIntegration(t *testing.T,
	constructor func(ed text.Editor, mu *sync.Mutex) (*text.Component, browserapi.WindowManager, error)) {
	t.Run("switches to a tab upon call to SetContent", func(t *testing.T) {
		cases := []handlertest.SequenceTestCase{
			{"",
				`┌──────━━━━────────┐
│x $$  x ##        │
├──────────────────┤
│##################│
│##################│
│##################│
│##################│
│##################│
│##################│
└──────────────────┘`},
		}

		fn := func(t *testing.T) tui.Handler {
			var mu sync.Mutex
			c, wm, err := constructor(texttest.NopEditor(), &mu)
			require.NoError(t, err)

			resource1, err := workspaceapi.ParseURI("file:///a")
			require.NoError(t, err)
			resource2, err := workspaceapi.ParseURI("file:///b")
			require.NoError(t, err)
			b1 := browsertest.NewTestHandler()
			b1.TestHandler.Ch = '$'
			_, err = wm.Tab(resource1, 'x', "$$", b1)
			require.NoError(t, err)

			b2 := browsertest.NewTestHandler()
			b2.TestHandler.Ch = '#'
			t2, err := wm.Tab(resource2, 'x', "##", b2)
			require.NoError(t, err)

			win, err := wm.Focus()
			require.NoError(t, err)

			require.NoError(t, wm.SetWindowContent(win, t2))
			return handler.Sync(&mu, handler.NopFromComponent(c.Browser()))
		}
		handlertest.TestHandlerIsolated(t, fn, 20, 10, cases)
	})
}

type testLoader struct {
	content       string
	flusherCloser *testFlusherCloser
	expectError   error
}

type testFlusherCloser struct {
	closeFn   func() error
	flushFn   func() error
	lastFlush time.Time
}

func (t *testLoader) StartCommand(context.Context, workspaceapi.Cmd) (workspaceapi.Pid, error) {
	panic("unimplemented")
}

func (t *testLoader) Signal(workspaceapi.Pid, syscall.Signal) error {
	panic("unimplemented")
}

func (t *testLoader) Close() error {
	panic("unimplemented")
}

func (t *testFlusherCloser) Close() error {
	if t.closeFn != nil {
		return t.closeFn()
	}
	return nil
}
func (t *testFlusherCloser) Reload(context.Context) (<-chan error, error) {
	return testDoneChanErr(nil), nil
}
func (t *testFlusherCloser) Flush(context.Context) (<-chan error, error) {
	t.lastFlush = time.Now()
	var err error
	if t.flushFn != nil {
		err = t.flushFn()
	}
	return testDoneChanErr(err), nil
}

func (t *testFlusherCloser) ForceFlush(context.Context) (<-chan error, error) {
	panic("unimplemented")
}

func testDoneChanErr(err error) <-chan error {
	ch := make(chan error, 1)
	ch <- err
	close(ch)
	return ch
}

func (t *testFlusherCloser) LastFlush() time.Time {
	return t.lastFlush
}

func (t *testLoader) Remove(string) error {
	return nil
}

func (t *testLoader) Load(
	file workspaceapi.URI, buf *cell.Buffer, swapDir workspaceapi.URI, readOnly bool,
) (workspace.FlusherCloser, error) {
	if t.expectError != nil {
		return nil, t.expectError
	}
	if t.flusherCloser != nil {
		return t.flusherCloser, nil
	}
	if t.content != "" {
		buf.WriteString(t.content)
	}
	return &testFlusherCloser{}, nil
}

func (t *testLoader) Recover(
	file, swapFilePath workspaceapi.URI, buf *cell.Buffer, force bool,
) (workspace.FlusherCloser, error) {
	return t.Load(file, buf, workspaceapi.URI{}, false)
}
func (t *testLoader) URI(path string) (workspaceapi.URI, error) {
	panic("unused")
}

func (t *testLoader) OpenFile(path string, flag int, perm os.FileMode) (
	workspaceapi.File, error,
) {
	panic("unused")
}

func (t *testLoader) Open(path string) (workspaceapi.File, error) {
	panic("unused")
}

func (t *testLoader) Stat(path string) (os.FileInfo, error) {
	panic("unused")
}

func (t *testLoader) ReadDir(name string) ([]os.DirEntry, error) {
	panic("unused")
}

func newTestComponentErr(ed text.Editor) (*text.Component, error) {
	cfg := text.DefaultConfig()
	cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
	c, err := text.NewComponent(ed, &testLoader{}, cfg)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func doSetupIntTest(
	t *testing.T, register func(*grpc.Server),
) (conn *grpc.ClientConn, closeFn func()) {
	lis, err := net.Listen("tcp", ":0")
	require.NoError(t, err)

	grpcServer := grpc.NewServer()
	register(grpcServer)

	go grpcServer.Serve(lis)

	conn, err = grpc.Dial(lis.Addr().String(), grpc.WithInsecure())
	require.NoError(t, err)

	closeFn = func() {
		grpcServer.Stop()
		lis.Close()
	}
	return
}

func setupIntTest(
	t *testing.T, s *Server,
) (*Client, func()) {
	conn, closeFn := doSetupIntTest(t, func(grpcServer *grpc.Server) {
		textrpc.RegisterEditorServer(grpcServer, s)
	})
	client := NewClient(context.Background(), conn)
	return client, func() {
		client.Close()
		closeFn()
	}
}

func setupWmIntTest(
	t *testing.T, s *tbrowserrpc.Server,
) (*browserrpc.Client, func()) {
	conn, closeFn := doSetupIntTest(t, func(grpcServer *grpc.Server) {
		browserrpc.RegisterWindowManagerServer(grpcServer, s)
	})
	client := browserrpc.NewClient(context.Background(), conn)
	return client, func() {
		client.Close()
		closeFn()
	}
}

type nopNotifications struct{}

func (n nopNotifications) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return "", nil
}
func (n nopNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return "", nil
}

func (n nopNotifications) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	return nil
}
