package main

const (
	deliveryGraphqlRootTemplate = `package graphqlhandler

import (
	"{{.PackageName}}/pkg/codebase/interfaces"
)

// GraphQLHandler model
type GraphQLHandler struct {
	query        *queryResolver
	mutation     *mutationResolver
	subscription *subscriptionResolver
}

// NewGraphQLHandler delivery
func NewGraphQLHandler(mw interfaces.Middleware) *GraphQLHandler {

	h := &GraphQLHandler{
		query:        &queryResolver{},
		mutation:     &mutationResolver{},
		subscription: &subscriptionResolver{},
	}

	return h
}

// Query method
func (h *GraphQLHandler) Query() interface{} {
	return h.query
}

// Mutation method
func (h *GraphQLHandler) Mutation() interface{} {
	return h.mutation
}

// Subscription method
func (h *GraphQLHandler) Subscription() interface{} {
	return h.subscription
}
`

	deliveryGraphqlQueryTemplate = `package graphqlhandler

import (
	"context"

	"{{.PackageName}}/pkg/tracer"
)

type queryResolver struct {
}

// Hello resolver
func (q *queryResolver) Hello(ctx context.Context) (string, error) {
	trace := tracer.StartTrace(ctx, "Delivery-Hello")
	defer trace.Finish()

	return "Hello, from service: {{$.ServiceName}}, module: {{$.module}}", nil
}
`
	deliveryGraphqlMutationTemplate = `package graphqlhandler

type mutationResolver struct {
}
`
	deliveryGraphqlSubscriptionTemplate = `package graphqlhandler

type subscriptionResolver struct {
}
`

	defaultGraphqlRootSchema = `schema {
	query: Query
	mutation: Mutation
	subscription: Subscription
}

type Query {
{{- range $module := .Modules}}
	{{clean $module}}: {{clean (upper $module)}}QueryModule
{{- end }}
}

type Mutation {
{{- range $module := .Modules}}
	{{clean $module}}: {{clean (upper $module)}}MutationModule
{{- end }}
}

type Subscription {
{{- range $module := .Modules}}
	{{clean $module}}: {{clean (upper $module)}}SubscriptionModule
{{- end }}
}
`

	defaultGraphqlSchema = `################### {{clean (upper $.module)}}Module Module Area
type {{clean (upper $.module)}}QueryModule {
    hello(): String!
}

type {{clean (upper $.module)}}MutationModule {
}

type {{clean (upper $.module)}}SubscriptionModule {
}
`
)
