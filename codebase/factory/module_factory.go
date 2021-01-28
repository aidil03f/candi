package factory

import (
	"pkg.agungdwiprasetyo.com/candi/codebase/factory/types"
	"pkg.agungdwiprasetyo.com/candi/codebase/interfaces"
)

// ModuleFactory factory
type ModuleFactory interface {
	RESTHandler() interfaces.RESTHandler
	GRPCHandler() interfaces.GRPCHandler
	GraphQLHandler() interfaces.GraphQLHandler
	WorkerHandler(workerType types.Worker) interfaces.WorkerHandler
	Name() types.Module
}
