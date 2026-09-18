package app

import (
	"context"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/stubbedev/harness/internal/agent/notify"
	"github.com/stubbedev/harness/internal/pubsub"
	"github.com/stubbedev/harness/internal/question"
)

// NewForTest constructs a minimal [App] suitable for in-process tests
// that need a working event broker and question service without
// booting a real config, database, LSP, MCP, or agent coordinator.
//
// The returned App has:
//
//   - A live `events` broker that [App.SendEvent] publishes to and
//     [App.Events] subscribes from.
//   - An [App.agentNotifications] broker.
//
// The caller owns lifetime: cancel ctx (or call [App.Shutdown]) to
// tear down the fan-in goroutines and the events broker.
func NewForTest(ctx context.Context) *App {
	app := &App{
		Questions:          question.NewService(),
		globalCtx:          ctx,
		events:             pubsub.NewStreamingBroker[tea.Msg](),
		serviceEventsWG:    &sync.WaitGroup{},
		tuiWG:              &sync.WaitGroup{},
		agentNotifications: pubsub.NewBroker[notify.Notification](),
		runCompletions:     pubsub.NewBroker[notify.RunComplete](),
	}

	eventsCtx, cancel := context.WithCancel(ctx)
	app.eventsCtx = eventsCtx
	app.subscribeMustDeliver(eventsCtx, "question-batches",
		app.Questions.Subscribe)
	app.subscribeMustDeliver(eventsCtx, "question-notifications",
		app.Questions.SubscribeNotifications)
	app.subscribe(eventsCtx, "agent-notifications",
		app.agentNotifications.Subscribe)
	app.subscribe(eventsCtx, "run-completions",
		app.runCompletions.Subscribe)
	app.cleanupFuncs = append(app.cleanupFuncs, func(context.Context) error {
		cancel()
		app.serviceEventsWG.Wait()
		app.events.Shutdown()
		return nil
	})
	return app
}

// ShutdownForTest tears down the App's event broker and fan-in
// goroutines. It is safe to call multiple times.
//
// Use this in tests instead of [App.Shutdown], which drives a full
// production shutdown path (database release, LSP teardown, MCP
// shutdown) that synthetic test apps cannot satisfy.
func (app *App) ShutdownForTest() {
	for _, cleanup := range app.cleanupFuncs {
		if cleanup != nil {
			_ = cleanup(context.Background())
		}
	}
	app.cleanupFuncs = nil
}
