// Code generated by mockery v2.13.1. DO NOT EDIT.

package mocks

import (
	context "context"

	mock "github.com/stretchr/testify/mock"

	types "github.com/golangid/graphql-go/types"
)

// GraphQLMiddleware is an autogenerated mock type for the GraphQLMiddleware type
type GraphQLMiddleware struct {
	mock.Mock
}

// GraphQLAuth provides a mock function with given fields: ctx, directive, input
func (_m *GraphQLMiddleware) GraphQLAuth(ctx context.Context, directive *types.Directive, input interface{}) (context.Context, error) {
	ret := _m.Called(ctx, directive, input)

	var r0 context.Context
	if rf, ok := ret.Get(0).(func(context.Context, *types.Directive, interface{}) context.Context); ok {
		r0 = rf(ctx, directive, input)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(context.Context)
		}
	}

	var r1 error
	if rf, ok := ret.Get(1).(func(context.Context, *types.Directive, interface{}) error); ok {
		r1 = rf(ctx, directive, input)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// GraphQLPermissionACL provides a mock function with given fields: ctx, directive, input
func (_m *GraphQLMiddleware) GraphQLPermissionACL(ctx context.Context, directive *types.Directive, input interface{}) (context.Context, error) {
	ret := _m.Called(ctx, directive, input)

	var r0 context.Context
	if rf, ok := ret.Get(0).(func(context.Context, *types.Directive, interface{}) context.Context); ok {
		r0 = rf(ctx, directive, input)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(context.Context)
		}
	}

	var r1 error
	if rf, ok := ret.Get(1).(func(context.Context, *types.Directive, interface{}) error); ok {
		r1 = rf(ctx, directive, input)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

type mockConstructorTestingTNewGraphQLMiddleware interface {
	mock.TestingT
	Cleanup(func())
}

// NewGraphQLMiddleware creates a new instance of GraphQLMiddleware. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
func NewGraphQLMiddleware(t mockConstructorTestingTNewGraphQLMiddleware) *GraphQLMiddleware {
	mock := &GraphQLMiddleware{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
