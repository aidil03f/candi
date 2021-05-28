// Code generated by mockery v0.0.0-dev. DO NOT EDIT.

package mocks

import (
	context "context"

	mock "github.com/stretchr/testify/mock"
)

// GraphQLService is an autogenerated mock type for the GraphQLService type
type GraphQLService struct {
	mock.Mock
}

// Subscribe provides a mock function with given fields: ctx, document, operationName, variableValues
func (_m *GraphQLService) Subscribe(ctx context.Context, document string, operationName string, variableValues map[string]interface{}) (<-chan interface{}, error) {
	ret := _m.Called(ctx, document, operationName, variableValues)

	var r0 <-chan interface{}
	if rf, ok := ret.Get(0).(func(context.Context, string, string, map[string]interface{}) <-chan interface{}); ok {
		r0 = rf(ctx, document, operationName, variableValues)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(<-chan interface{})
		}
	}

	var r1 error
	if rf, ok := ret.Get(1).(func(context.Context, string, string, map[string]interface{}) error); ok {
		r1 = rf(ctx, document, operationName, variableValues)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}