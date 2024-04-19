// Code generated by mockery v2.36.1. DO NOT EDIT.

package actions

import (
	context "context"

	mock "github.com/stretchr/testify/mock"
)

// MockSessionService is an autogenerated mock type for the SessionService type
type MockSessionService struct {
	mock.Mock
}

// AcquireJobs provides a mock function with given fields: ctx, requestIds
func (_m *MockSessionService) AcquireJobs(ctx context.Context, requestIds []int64) ([]int64, error) {
	ret := _m.Called(ctx, requestIds)

	var r0 []int64
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, []int64) ([]int64, error)); ok {
		return rf(ctx, requestIds)
	}
	if rf, ok := ret.Get(0).(func(context.Context, []int64) []int64); ok {
		r0 = rf(ctx, requestIds)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]int64)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, []int64) error); ok {
		r1 = rf(ctx, requestIds)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Close provides a mock function with given fields:
func (_m *MockSessionService) Close() error {
	ret := _m.Called()

	var r0 error
	if rf, ok := ret.Get(0).(func() error); ok {
		r0 = rf()
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// DeleteMessage provides a mock function with given fields: ctx, messageId
func (_m *MockSessionService) DeleteMessage(ctx context.Context, messageId int64) error {
	ret := _m.Called(ctx, messageId)

	var r0 error
	if rf, ok := ret.Get(0).(func(context.Context, int64) error); ok {
		r0 = rf(ctx, messageId)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// GetMessage provides a mock function with given fields: ctx, lastMessageId, maxCapacity
func (_m *MockSessionService) GetMessage(ctx context.Context, lastMessageId int64, maxCapacity int) (*RunnerScaleSetMessage, error) {
	ret := _m.Called(ctx, lastMessageId, maxCapacity)

	var r0 *RunnerScaleSetMessage
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, int64, int) (*RunnerScaleSetMessage, error)); ok {
		return rf(ctx, lastMessageId, maxCapacity)
	}
	if rf, ok := ret.Get(0).(func(context.Context, int64, int) *RunnerScaleSetMessage); ok {
		r0 = rf(ctx, lastMessageId, maxCapacity)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*RunnerScaleSetMessage)
		}
	}

	if rf, ok := ret.Get(1).(func(context.Context, int64, int) error); ok {
		r1 = rf(ctx, lastMessageId, maxCapacity)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// NewMockSessionService creates a new instance of MockSessionService. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewMockSessionService(t interface {
	mock.TestingT
	Cleanup(func())
}) *MockSessionService {
	mock := &MockSessionService{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
