package threads

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/memory"
)

func (s Store) callBroker(
	ctx context.Context,
	operation string,
	input,
	output any,
	mutation bool,
) error {
	if s.brokerClient == nil {
		return database.NewError(database.CodeUnavailable, "thread broker client is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if mutation {
		return s.brokerClient.CallWithOptions(
			ctx, memory.SessionsBrokerDomain, memory.SessionsBrokerVersion,
			operation, input, output, database.CallOptions{Mutation: true},
		)
	}
	return s.brokerClient.Call(
		ctx, memory.SessionsBrokerDomain, memory.SessionsBrokerVersion, operation, input, output,
	)
}

func (s Store) resolvedBrokerStoreID(ctx context.Context) (database.StoreID, error) {
	if s.brokerStoreID.Valid() {
		return s.brokerStoreID, nil
	}
	if s.brokerResolveErr != nil {
		return "", s.brokerResolveErr
	}
	if s.brokerClient == nil {
		return "", database.NewError(database.CodeUnavailable, "thread broker client is unavailable")
	}
	return memory.ResolveBrokerStoreID(ctx, s.brokerClient, s.withDefaults().Dir)
}

func (s Store) brokerSearch(ctx context.Context, options SearchOptions) ([]Thread, error) {
	storeID, err := s.resolvedBrokerStoreID(ctx)
	if err != nil {
		return nil, err
	}
	var response threadBrokerResponse
	err = s.callBroker(
		ctx, threadOperationSearch,
		threadSearchRequest{StoreID: storeID, Options: options},
		&response, false,
	)
	return cloneThreads(response.Threads), err
}

func (s Store) brokerList(ctx context.Context, options ListOptions) ([]Thread, error) {
	storeID, err := s.resolvedBrokerStoreID(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Thread, 0)
	offset := 0
	for {
		var response threadBrokerResponse
		err := s.callBroker(
			ctx, threadOperationList,
			threadListRequest{
				StoreID: storeID, Options: options,
				Offset: offset, Limit: threadBrokerPageLimit,
			},
			&response, false,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, cloneThreads(response.Threads)...)
		if response.Next == 0 {
			return result, nil
		}
		if response.Next <= offset {
			return nil, database.NewError(database.CodeIntegrity, "thread broker page is invalid")
		}
		offset = response.Next
	}
}

func (s Store) brokerGet(ctx context.Context, id string) (Thread, bool, error) {
	storeID, err := s.resolvedBrokerStoreID(ctx)
	if err != nil {
		return Thread{}, false, err
	}
	var response threadBrokerResponse
	err = s.callBroker(
		ctx, threadOperationGet,
		threadIDRequest{StoreID: storeID, ID: id}, &response, false,
	)
	if err != nil || !response.Found {
		return Thread{}, response.Found, err
	}
	if response.Thread == nil {
		return Thread{}, false, database.NewError(database.CodeIntegrity, "thread broker response is invalid")
	}
	return cloneThread(*response.Thread), true, nil
}

func (s Store) brokerGetMeta(ctx context.Context, id string) (ThreadMeta, bool, error) {
	storeID, err := s.resolvedBrokerStoreID(ctx)
	if err != nil {
		return ThreadMeta{}, false, err
	}
	var response threadBrokerResponse
	err = s.callBroker(
		ctx, threadOperationGetMeta,
		threadIDRequest{StoreID: storeID, ID: id}, &response, false,
	)
	if err != nil || !response.Found {
		return ThreadMeta{}, response.Found, err
	}
	if response.Meta == nil {
		return ThreadMeta{}, false, database.NewError(database.CodeIntegrity, "thread broker response is invalid")
	}
	return cloneThreadMeta(*response.Meta), true, nil
}

func (s Store) brokerThreadMutation(
	ctx context.Context,
	operation string,
	input any,
) (Thread, bool, error) {
	var response threadBrokerResponse
	err := s.callBroker(ctx, operation, input, &response, true)
	if err != nil || !response.Found {
		return Thread{}, response.Found, err
	}
	if response.Thread == nil {
		return Thread{}, false, database.NewError(database.CodeIntegrity, "thread broker response is invalid")
	}
	return cloneThread(*response.Thread), true, nil
}
