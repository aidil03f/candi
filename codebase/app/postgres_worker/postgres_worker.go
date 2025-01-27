package postgresworker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime/debug"
	"sync"

	"github.com/golangid/candi/candihelper"
	"github.com/golangid/candi/candishared"
	"github.com/golangid/candi/codebase/factory"
	"github.com/golangid/candi/codebase/factory/types"
	"github.com/golangid/candi/logger"
	"github.com/golangid/candi/tracer"
	"github.com/lib/pq"
)

/*
Postgres Event Listener Worker
Listen event from data change from selected table in postgres
*/

type (
	postgresWorker struct {
		ctx           context.Context
		ctxCancelFunc func()
		opt           option
		semaphore     map[string]chan struct{}
		shutdown      chan struct{}

		service  factory.ServiceFactory
		listener *pq.Listener
		handlers map[string]types.WorkerHandler
		wg       sync.WaitGroup
	}
)

// NewWorker create new postgres event listener
func NewWorker(service factory.ServiceFactory, opts ...OptionFunc) factory.AppServerFactory {
	worker := &postgresWorker{
		service:   service,
		opt:       getDefaultOption(service),
		semaphore: make(map[string]chan struct{}),
		shutdown:  make(chan struct{}, 1),
	}

	for _, opt := range opts {
		opt(&worker.opt)
	}

	worker.handlers = make(map[string]types.WorkerHandler)
	db, listener := getListener(worker.opt.postgresDSN)
	execCreateFunctionEventQuery(db)

	worker.opt.locker.Reset(fmt.Sprintf("%s:postgres-worker-lock:*", service.Name()))
	for _, m := range service.GetModules() {
		if h := m.WorkerHandler(types.PostgresListener); h != nil {
			var handlerGroup types.WorkerHandlerGroup
			h.MountHandlers(&handlerGroup)
			for _, handler := range handlerGroup.Handlers {
				logger.LogYellow(fmt.Sprintf(`[POSTGRES-LISTENER] (table): %-15s  --> (module): "%s"`, `"`+handler.Pattern+`"`, m.Name()))
				worker.handlers[handler.Pattern] = handler
				worker.semaphore[handler.Pattern] = make(chan struct{}, worker.opt.maxGoroutines)
				execTriggerQuery(db, handler.Pattern)
			}
		}
	}

	if len(worker.handlers) == 0 {
		log.Println("postgres listener: no table event provided")
	} else {
		fmt.Printf("\x1b[34;1m⇨ Postgres Event Listener running with %d table. DSN: %s\x1b[0m\n\n",
			len(worker.handlers), candihelper.MaskingPasswordURL(worker.opt.postgresDSN))
	}

	worker.listener = listener
	worker.ctx, worker.ctxCancelFunc = context.WithCancel(context.Background())
	return worker
}

func (p *postgresWorker) Serve() {
	p.listener.Listen(eventsConst)

	for {
		select {
		case e := <-p.listener.Notify:

			var payload EventPayload
			json.Unmarshal([]byte(e.Extra), &payload)

			p.semaphore[payload.Table] <- struct{}{}
			p.wg.Add(1)
			go func(data *EventPayload) {
				defer func() { p.wg.Done(); <-p.semaphore[data.Table] }()

				if p.ctx.Err() != nil {
					logger.LogRed("postgres_listener > ctx root err: " + p.ctx.Err().Error())
					return
				}

				ctx := p.ctx

				// lock for multiple worker (if running on multiple pods/instance)
				if p.opt.locker.IsLocked(p.getLockKey(data)) {
					return
				}
				defer p.opt.locker.Unlock(p.getLockKey(data))

				handler := p.handlers[data.Table]
				if handler.DisableTrace {
					ctx = tracer.SkipTraceContext(ctx)
				}
				trace, ctx := tracer.StartTraceFromHeader(ctx, "PostgresEventListener", map[string]string{})
				defer func() {
					if r := recover(); r != nil {
						trace.SetError(fmt.Errorf("panic: %v", r))
						trace.Log("stacktrace", string(debug.Stack()))
					}
					logger.LogGreen("postgres_listener > trace_url: " + tracer.GetTraceURL(ctx))
					trace.SetTag("trace_id", tracer.GetTraceID(ctx))
					trace.Finish()
				}()

				if p.opt.debugMode {
					log.Printf("\x1b[35;3mPostgres Event Listener: executing event from table: '%s' and action: '%s'\x1b[0m", data.Table, data.Action)
				}

				trace.SetTag("database", candihelper.MaskingPasswordURL(p.opt.postgresDSN))
				trace.SetTag("table_name", data.Table)
				trace.SetTag("action", data.Action)
				trace.Log("payload", data)
				message, _ := json.Marshal(data)

				var eventContext candishared.EventContext
				eventContext.SetContext(ctx)
				eventContext.SetWorkerType(string(types.PostgresListener))
				eventContext.SetHandlerRoute(data.Table)
				eventContext.SetKey(data.EventID)
				eventContext.Write(message)

				for _, handlerFunc := range handler.HandlerFuncs {
					if err := handlerFunc(&eventContext); err != nil {
						eventContext.SetError(err)
						trace.SetError(err)
					}
				}
			}(&payload)

		case <-p.shutdown:
			return
		}
	}
}

func (p *postgresWorker) Shutdown(ctx context.Context) {
	defer func() {
		log.Println("\x1b[33;1mStopping Postgres Event Listener:\x1b[0m \x1b[32;1mSUCCESS\x1b[0m")
	}()

	if len(p.handlers) == 0 {
		return
	}

	p.shutdown <- struct{}{}
	runningJob := 0
	for _, sem := range p.semaphore {
		runningJob += len(sem)
	}
	if runningJob != 0 {
		fmt.Printf("\x1b[34;1mPostgres Event Listener:\x1b[0m waiting %d job until done...\n", runningJob)
	}

	p.listener.Unlisten(eventsConst)
	p.listener.Close()
	p.wg.Wait()
	p.ctxCancelFunc()
	p.opt.locker.Reset(fmt.Sprintf("%s:postgres-worker-lock:*", p.service.Name()))
}

func (p *postgresWorker) Name() string {
	return string(types.PostgresListener)
}

func (p *postgresWorker) getLockKey(eventPayload *EventPayload) string {
	return fmt.Sprintf("%s:postgres-worker-lock:%s-%s-%s", p.service.Name(), eventPayload.Table, eventPayload.Action, eventPayload.EventID)
}
