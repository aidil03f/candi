package taskqueueworker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	dashboard "github.com/golangid/candi-plugin/task-queue-worker/dashboard"
	"github.com/golangid/candi/candihelper"
	"github.com/golangid/candi/logger"
	"github.com/golangid/graphql-go"
	"github.com/golangid/graphql-go/relay"
	"github.com/golangid/graphql-go/trace"

	"github.com/golangid/candi"
	"github.com/golangid/candi/candishared"
	"github.com/golangid/candi/codebase/app/graphql_server/static"
	"github.com/golangid/candi/codebase/app/graphql_server/ws"
	"github.com/golangid/candi/config/env"
)

func (t *taskQueueWorker) serveGraphQLAPI() {
	schemaOpts := []graphql.SchemaOpt{
		graphql.UseStringDescriptions(),
		graphql.UseFieldResolvers(),
		graphql.Tracer(trace.NoopTracer{}),
	}
	schema := graphql.MustParseSchema(schema, &rootResolver{engine: t}, schemaOpts...)

	mux := http.NewServeMux()
	mux.Handle("/", http.StripPrefix("/", http.FileServer(dashboard.Dashboard)))
	mux.Handle("/task", http.StripPrefix("/", http.FileServer(dashboard.Dashboard)))
	mux.Handle("/job", http.StripPrefix("/", http.FileServer(dashboard.Dashboard)))
	mux.Handle("/expired", http.StripPrefix("/", http.FileServer(dashboard.Dashboard)))

	mux.HandleFunc("/graphql", ws.NewHandlerFunc(schema, &relay.Handler{Schema: schema}))
	mux.HandleFunc("/voyager", func(rw http.ResponseWriter, r *http.Request) { rw.Write([]byte(static.VoyagerAsset)) })
	mux.HandleFunc("/playground", func(rw http.ResponseWriter, r *http.Request) { rw.Write([]byte(static.PlaygroundAsset)) })

	httpEngine := new(http.Server)
	httpEngine.Addr = fmt.Sprintf(":%d", engine.opt.dashboardPort)
	httpEngine.Handler = mux

	if err := httpEngine.ListenAndServe(); err != nil {
		panic(fmt.Errorf("task queue worker dashboard: %v", err))
	}
}

type rootResolver struct {
	engine *taskQueueWorker
}

func (r *rootResolver) Dashboard(ctx context.Context, input struct{ GC *bool }) (res DashboardResolver) {

	res.Banner = r.engine.opt.dashboardBanner
	res.Tagline = "Task Queue Worker Dashboard"
	res.Version = candi.Version
	res.GoVersion = runtime.Version()
	res.StartAt = env.BaseEnv().StartAt
	res.BuildNumber = env.BaseEnv().BuildNumber
	res.MemoryStatistics = getMemstats()

	_, isType := r.engine.opt.persistent.(*noopPersistent)
	res.Config.WithPersistent = !isType

	if input.GC != nil && *input.GC {
		runtime.GC()
	}

	// dependency health
	if err := r.engine.opt.persistent.Ping(ctx); err != nil {
		res.DependencyHealth.Persistent = candihelper.ToStringPtr(err.Error())
	}
	if err := r.engine.opt.queue.Ping(); err != nil {
		res.DependencyHealth.Queue = candihelper.ToStringPtr(err.Error())
	}

	res.DependencyDetail.PersistentType = r.engine.opt.persistent.Type()
	res.DependencyDetail.QueueType = r.engine.opt.queue.Type()
	_, isType = r.engine.opt.secondaryPersistent.(*noopPersistent)
	res.DependencyDetail.UseSecondaryPersistent = !isType

	return
}

func (r *rootResolver) GetAllJob(ctx context.Context, input struct{ Filter *GetAllJobInputResolver }) (result JobListResolver, err error) {

	if input.Filter == nil {
		input.Filter = &GetAllJobInputResolver{}
	}

	filter := input.Filter.ToFilter()
	result.GetAllJob(ctx, &filter)
	return
}

func (r *rootResolver) GetDetailJob(ctx context.Context, input struct {
	JobID  string
	Filter *GetAllJobHistoryInputResolver
}) (res JobResolver, err error) {

	if input.Filter == nil {
		input.Filter = &GetAllJobHistoryInputResolver{}
	}
	filter := input.Filter.ToFilter()
	job, err := r.engine.opt.persistent.FindJobByID(ctx, input.JobID, &filter)
	if err != nil {
		return res, err
	}
	res.ParseFromJob(&job, -1)
	res.Meta.Page = filter.Page
	res.Meta.TotalHistory = filter.Count
	return
}

func (r *rootResolver) DeleteJob(ctx context.Context, input struct{ JobID string }) (ok string, err error) {
	job, err := r.engine.opt.persistent.DeleteJob(ctx, input.JobID)
	if err != nil {
		return "", err
	}
	r.engine.opt.persistent.Summary().IncrementSummary(ctx, job.TaskName, map[string]int64{
		job.Status: -1,
	})
	r.engine.subscriber.broadcastAllToSubscribers(r.engine.ctx)
	return
}

func (r *rootResolver) AddJob(ctx context.Context, input struct{ Param AddJobInputResolver }) (string, error) {

	job := AddJobRequest{
		TaskName: input.Param.TaskName,
		MaxRetry: int(input.Param.MaxRetry),
		Args:     []byte(input.Param.Args),
		direct:   true,
	}
	if input.Param.RetryInterval != nil {
		interval, err := time.ParseDuration(*input.Param.RetryInterval)
		if err != nil {
			return "", err
		}
		job.RetryInterval = interval
	}
	return AddJob(ctx, &job)
}

func (r *rootResolver) StopJob(ctx context.Context, input struct {
	JobID string
}) (string, error) {

	return "Success stop job " + input.JobID, StopJob(r.engine.ctx, input.JobID)
}

func (r *rootResolver) RetryJob(ctx context.Context, input struct {
	JobID string
}) (string, error) {

	return "Success retry job " + input.JobID, RetryJob(r.engine.ctx, input.JobID)
}

func (r *rootResolver) StopAllJob(ctx context.Context, input struct {
	TaskName string
}) (string, error) {

	if _, ok := r.engine.registeredTaskWorkerIndex[input.TaskName]; !ok {
		return "", fmt.Errorf("task '%s' unregistered, task must one of [%s]",
			input.TaskName, strings.Join(r.engine.tasks, ", "))
	}

	r.engine.subscriber.broadcastWhenChangeAllJob(r.engine.ctx, input.TaskName, true, "Stopping...")
	r.engine.opt.queue.Clear(ctx, input.TaskName)
	go r.engine.stopAllJobInTask(input.TaskName)

	incrQuery := map[string]int64{}
	affectedStatus := []string{string(statusQueueing), string(statusRetrying)}
	for _, status := range affectedStatus {
		countMatchedFilter, countAffected, err := r.engine.opt.persistent.UpdateJob(ctx,
			&Filter{
				TaskName: input.TaskName, Status: &status,
			},
			map[string]interface{}{
				"status": statusStopped,
			},
		)
		if err != nil {
			continue
		}
		incrQuery[strings.ToLower(status)] -= countMatchedFilter
		incrQuery[strings.ToLower(string(statusStopped))] += countAffected
	}

	r.engine.subscriber.broadcastWhenChangeAllJob(r.engine.ctx, input.TaskName, false, "")
	r.engine.opt.persistent.Summary().IncrementSummary(ctx, input.TaskName, incrQuery)
	r.engine.subscriber.broadcastAllToSubscribers(r.engine.ctx)

	return "Success stop all job in task " + input.TaskName, nil
}

func (r *rootResolver) RetryAllJob(ctx context.Context, input struct {
	TaskName string
}) (string, error) {

	go func(ctx context.Context) {

		r.engine.subscriber.broadcastWhenChangeAllJob(ctx, input.TaskName, true, "Retrying...")

		filter := &Filter{
			Page: 1, Limit: 10, Sort: "created_at",
			Statuses: []string{string(statusFailure), string(statusStopped)}, TaskName: input.TaskName,
		}
		StreamAllJob(ctx, filter, func(job *Job) {
			r.engine.opt.queue.PushJob(ctx, job)
		})

		incr := map[string]int64{}
		for _, status := range filter.Statuses {
			countMatchedFilter, countAffected, err := r.engine.opt.persistent.UpdateJob(ctx,
				&Filter{
					TaskName: input.TaskName, Status: &status,
				},
				map[string]interface{}{
					"status":  statusQueueing,
					"retries": 0,
				},
			)
			if err != nil {
				continue
			}
			incr[strings.ToLower(status)] -= countMatchedFilter
			incr[strings.ToLower(string(statusQueueing))] += countAffected
		}

		r.engine.subscriber.broadcastWhenChangeAllJob(ctx, input.TaskName, false, "")
		r.engine.opt.persistent.Summary().IncrementSummary(ctx, input.TaskName, incr)
		r.engine.subscriber.broadcastAllToSubscribers(r.engine.ctx)
		r.engine.registerNextJob(false, input.TaskName)

	}(r.engine.ctx)

	return "Success retry all failure job in task " + input.TaskName, nil
}

func (r *rootResolver) CleanJob(ctx context.Context, input struct {
	TaskName string
}) (string, error) {

	go func(ctx context.Context) {

		r.engine.subscriber.broadcastWhenChangeAllJob(ctx, input.TaskName, true, "Cleaning...")

		incrQuery := map[string]int64{}
		affectedStatus := []string{string(statusSuccess), string(statusFailure), string(statusStopped)}
		for _, status := range affectedStatus {
			countAffected := r.engine.opt.persistent.CleanJob(ctx,
				&Filter{
					TaskName: input.TaskName, Status: &status,
				},
			)
			incrQuery[strings.ToLower(status)] -= countAffected
		}

		r.engine.subscriber.broadcastWhenChangeAllJob(ctx, input.TaskName, false, "")
		r.engine.opt.persistent.Summary().IncrementSummary(ctx, input.TaskName, incrQuery)
		r.engine.subscriber.broadcastAllToSubscribers(r.engine.ctx)

	}(r.engine.ctx)

	return "Success clean all job in task " + input.TaskName, nil
}

func (r *rootResolver) RecalculateSummary(ctx context.Context) (string, error) {

	RecalculateSummary(ctx)
	r.engine.subscriber.broadcastAllToSubscribers(r.engine.ctx)
	return "Success recalculate summary", nil
}

func (r *rootResolver) ClearAllClientSubscriber(ctx context.Context) (string, error) {

	go func() {
		for k := range r.engine.subscriber.clientTaskSubscribers {
			r.engine.subscriber.removeTaskListSubscriber(k)
			r.engine.subscriber.closeAllSubscribers <- struct{}{}
		}
		for k := range r.engine.subscriber.clientTaskJobListSubscribers {
			r.engine.subscriber.removeJobListSubscriber(k)
			r.engine.subscriber.closeAllSubscribers <- struct{}{}
		}
		for k := range r.engine.subscriber.clientJobDetailSubscribers {
			r.engine.subscriber.removeJobDetailSubscriber(k)
			r.engine.subscriber.closeAllSubscribers <- struct{}{}
		}
	}()

	return "Success clear all client subscriber", nil
}

func (r *rootResolver) KillClientSubscriber(ctx context.Context, input struct{ ClientID string }) (string, error) {

	taskSubs, ok := r.engine.subscriber.clientTaskSubscribers[input.ClientID]
	if ok {
		taskSubs.c <- TaskListResolver{Meta: MetaTaskResolver{IsCloseSession: true}}
		r.engine.subscriber.removeTaskListSubscriber(input.ClientID)
	}
	jobListSubs, ok := r.engine.subscriber.clientTaskJobListSubscribers[input.ClientID]
	if ok {
		jobListSubs.c <- JobListResolver{Meta: MetaJobList{IsCloseSession: true}}
		r.engine.subscriber.removeJobListSubscriber(input.ClientID)
	}
	jobDetailSubs, ok := r.engine.subscriber.clientJobDetailSubscribers[input.ClientID]
	if ok {
		var js JobResolver
		js.Meta.IsCloseSession = true
		jobDetailSubs.c <- js
		r.engine.subscriber.removeJobDetailSubscriber(input.ClientID)
	}

	r.engine.subscriber.broadcastTaskList(ctx)
	return "Success kill client subscriber", nil
}

func (r *rootResolver) GetAllActiveSubscriber(ctx context.Context) (cs []*ClientSubscriber, err error) {

	mapper := make(map[string]*ClientSubscriber)
	for k := range r.engine.subscriber.clientTaskSubscribers {
		_, ok := mapper[k]
		if !ok {
			mapper[k] = &ClientSubscriber{}
		}
		mapper[k].ClientID = k
		mapper[k].PageName = "Root Dashboard"
	}
	for k, v := range r.engine.subscriber.clientTaskJobListSubscribers {
		_, ok := mapper[k]
		if !ok {
			mapper[k] = &ClientSubscriber{}
		}
		mapper[k].ClientID = k
		mapper[k].PageName = "Job List"
		mapper[k].PageFilter = string(candihelper.ToBytes(v.filter))
	}
	for k, v := range r.engine.subscriber.clientJobDetailSubscribers {
		_, ok := mapper[k]
		if !ok {
			mapper[k] = &ClientSubscriber{}
		}
		mapper[k].ClientID = k
		mapper[k].PageName = "Job Detail"
		mapper[k].PageFilter = candihelper.PtrToString(v.filter.JobID)
	}

	for _, v := range mapper {
		cs = append(cs, v)
	}

	sort.Slice(cs, func(i, j int) bool {
		return cs[i].PageName > cs[j].PageName
	})
	return cs, nil
}

func (r *rootResolver) GetAllConfiguration(ctx context.Context) (res []ConfigurationResolver, err error) {
	configs, err := r.engine.opt.persistent.GetAllConfiguration(ctx)
	if err != nil {
		return res, err
	}
	res = make([]ConfigurationResolver, 0)
	for _, cfg := range configs {
		res = append(res, ConfigurationResolver{
			Key: cfg.Key, Name: cfg.Name, Value: cfg.Value, IsActive: cfg.IsActive,
		})
	}
	return
}

func (r *rootResolver) GetDetailConfiguration(ctx context.Context, input struct{ Key string }) (res ConfigurationResolver, err error) {
	cfg, err := r.engine.opt.persistent.GetConfiguration(input.Key)
	if err != nil {
		return res, err
	}
	res = ConfigurationResolver{
		Key: cfg.Key, Name: cfg.Name, Value: cfg.Value, IsActive: cfg.IsActive,
	}
	return
}

func (r *rootResolver) SetConfiguration(ctx context.Context, input struct {
	Config ConfigurationResolver
}) (res string, err error) {

	if err := r.engine.configuration.setConfiguration(&Configuration{
		Key: input.Config.Key, Name: input.Config.Name, Value: input.Config.Value, IsActive: input.Config.IsActive,
	}); err != nil {
		return res, err
	}
	return "success", nil
}

func (r *rootResolver) RunQueuedJob(ctx context.Context, input struct {
	TaskName string
}) (res string, err error) {

	nextJobID := r.engine.opt.queue.NextJob(ctx, input.TaskName)
	if nextJobID != "" {

		nextJob, err := r.engine.opt.persistent.FindJobByID(ctx, nextJobID, nil)
		if err != nil {
			return "Failed find detail job", err
		}
		r.engine.registerJobToWorker(&nextJob)

		return "Success with job id " + nextJobID, nil
	}

	StreamAllJob(ctx, &Filter{
		TaskName: input.TaskName,
		Sort:     "created_at",
		Status:   candihelper.ToStringPtr(string(statusQueueing)),
	}, func(job *Job) {
		r.engine.opt.queue.PushJob(ctx, job)
	})

	r.engine.registerNextJob(false, input.TaskName)
	return "Success with stream all job", nil
}

func (r *rootResolver) RestoreFromSecondary(ctx context.Context) (res RestoreSecondaryResolver, err error) {

	filter := &Filter{
		Sort:                "created_at",
		secondaryPersistent: true,
		Status:              candihelper.ToStringPtr(string(statusQueueing)),
	}
	res.TotalData = StreamAllJob(ctx, filter, func(job *Job) {

		if err := r.engine.opt.persistent.SaveJob(ctx, job); err != nil {
			logger.LogE(err.Error())
			return
		}
		r.engine.opt.persistent.Summary().IncrementSummary(ctx, job.TaskName, map[string]int64{
			strings.ToLower(job.Status): 1,
		})
		if n := r.engine.opt.queue.PushJob(ctx, job); n <= 1 {
			r.engine.registerJobToWorker(job)
		}
	})

	r.engine.opt.secondaryPersistent.CleanJob(ctx, filter)
	r.engine.subscriber.broadcastAllToSubscribers(context.Background())
	res.Message = fmt.Sprintf("%d data restored", res.TotalData)
	return
}

func (r *rootResolver) ListenTaskDashboard(ctx context.Context, input struct {
	Page, Limit int
	Search      *string
}) (<-chan TaskListResolver, error) {
	output := make(chan TaskListResolver)

	httpHeader := candishared.GetValueFromContext(ctx, candishared.ContextKeyHTTPHeader).(http.Header)
	clientID := httpHeader.Get("Sec-WebSocket-Key")

	if err := r.engine.subscriber.registerNewTaskListSubscriber(clientID, &Filter{
		Page: input.Page, Limit: input.Limit, Search: input.Search,
	}, output); err != nil {
		return nil, err
	}

	autoRemoveClient := time.NewTicker(r.engine.configuration.getClientSubscriberAge())

	go r.engine.subscriber.broadcastTaskList(r.engine.ctx)

	go func() {
		defer func() { r.engine.subscriber.broadcastTaskList(r.engine.ctx); close(output); autoRemoveClient.Stop() }()

		select {
		case <-ctx.Done():
			r.engine.subscriber.removeTaskListSubscriber(clientID)
			return

		case <-r.engine.subscriber.closeAllSubscribers:
			output <- TaskListResolver{Meta: MetaTaskResolver{IsCloseSession: true}}
			r.engine.subscriber.removeTaskListSubscriber(clientID)
			return

		case <-autoRemoveClient.C:
			output <- TaskListResolver{Meta: MetaTaskResolver{IsCloseSession: true}}
			r.engine.subscriber.removeTaskListSubscriber(clientID)
			return
		}

	}()

	return output, nil
}

func (r *rootResolver) ListenAllJob(ctx context.Context, input struct{ Filter *GetAllJobInputResolver }) (<-chan JobListResolver, error) {

	output := make(chan JobListResolver)

	httpHeader := candishared.GetValueFromContext(ctx, candishared.ContextKeyHTTPHeader).(http.Header)
	clientID := httpHeader.Get("Sec-WebSocket-Key")

	if input.Filter == nil {
		input.Filter = &GetAllJobInputResolver{}
	}

	filter := input.Filter.ToFilter()

	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.Limit <= 0 || filter.Limit > 10 {
		filter.Limit = 10
	}

	if err := r.engine.subscriber.registerNewJobListSubscriber(clientID, &filter, output); err != nil {
		return nil, err
	}

	go r.engine.subscriber.broadcastJobListToClient(ctx, clientID)

	autoRemoveClient := time.NewTicker(r.engine.configuration.getClientSubscriberAge())
	go func() {
		defer func() { close(output); autoRemoveClient.Stop() }()

		select {
		case <-ctx.Done():
			r.engine.subscriber.removeJobListSubscriber(clientID)
			return

		case <-r.engine.subscriber.closeAllSubscribers:
			output <- JobListResolver{Meta: MetaJobList{IsCloseSession: true}}
			r.engine.subscriber.removeJobListSubscriber(clientID)
			return

		case <-autoRemoveClient.C:
			output <- JobListResolver{Meta: MetaJobList{IsCloseSession: true}}
			r.engine.subscriber.removeJobListSubscriber(clientID)
			return

		}
	}()

	return output, nil
}

func (r *rootResolver) ListenDetailJob(ctx context.Context, input struct {
	JobID  string
	Filter *GetAllJobHistoryInputResolver
}) (<-chan JobResolver, error) {

	output := make(chan JobResolver)

	httpHeader := candishared.GetValueFromContext(ctx, candishared.ContextKeyHTTPHeader).(http.Header)
	clientID := httpHeader.Get("Sec-WebSocket-Key")

	if input.JobID == "" {
		return output, errors.New("Job ID cannot empty")
	}

	_, err := r.engine.opt.persistent.FindJobByID(ctx, input.JobID, nil)
	if err != nil {
		return output, errors.New("Job not found")
	}

	if input.Filter == nil {
		input.Filter = &GetAllJobHistoryInputResolver{}
	}
	filter := input.Filter.ToFilter()
	filter.JobID = &input.JobID
	if err := r.engine.subscriber.registerNewJobDetailSubscriber(clientID, &filter, output); err != nil {
		return nil, err
	}

	go r.engine.subscriber.broadcastJobDetail(ctx)

	autoRemoveClient := time.NewTicker(r.engine.configuration.getClientSubscriberAge())
	go func() {
		defer func() { close(output); autoRemoveClient.Stop() }()

		select {
		case <-ctx.Done():
			r.engine.subscriber.removeJobDetailSubscriber(clientID)
			return

		case <-r.engine.subscriber.closeAllSubscribers:
			var js JobResolver
			js.Meta.IsCloseSession = true
			output <- js
			r.engine.subscriber.removeJobDetailSubscriber(clientID)
			return

		case <-autoRemoveClient.C:
			var js JobResolver
			js.Meta.IsCloseSession = true
			output <- js
			r.engine.subscriber.removeJobDetailSubscriber(clientID)
			return

		}
	}()

	return output, nil
}
