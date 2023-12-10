// Copyright (c) 2023 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package queue

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
)

type consumerFunc[T any] func(context.Context) (T, error)

func (f consumerFunc[T]) Consume(ctx context.Context) (T, error) {
	return f(ctx)
}

type processorFunc[T any] func(context.Context, T) error

func (f processorFunc[T]) Process(ctx context.Context, t T) error {
	return f(ctx, t)
}

func ExampleSequential() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var n int
	c := consumerFunc[int](func(_ context.Context) (int, error) {
		n += 1
		return n, nil
	})
	p := processorFunc[int](func(_ context.Context, n int) error {
		if n > 5 {
			cancel()
			return nil
		}
		// items are processed sequentially in this case so we can
		// compare based on the printed lines
		fmt.Println(n)
		return nil
	})

	rt := Sequential[int](c, p, LogHandler(slog.Default().Handler()))

	err := rt.Run(ctx)
	if err != nil {
		fmt.Println(err)
		return
	}

	//Output: 1
	// 2
	// 3
	// 4
	// 5
}

func ExamplePipe() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var n int
	c := consumerFunc[int](func(_ context.Context) (int, error) {
		n += 1
		return n, nil
	})

	var processed atomic.Int64
	var mu sync.Mutex
	var nums []int
	p := processorFunc[int](func(_ context.Context, n int) error {
		processed.Add(1)
		if processed.Load() > 5 {
			cancel()
			return nil
		}
		// items are processed concurrently so we can print them here
		// since the order is not gauranteed
		mu.Lock()
		nums = append(nums, n)
		mu.Unlock()
		return nil
	})

	rt := Pipe[int](c, p, LogHandler(slog.Default().Handler()))

	err := rt.Run(ctx)
	if err != nil {
		fmt.Println(err)
		return
	}

	// since the numbers are processed concurrently
	// there's no gaurantee that the list only contains
	// 1, 2, 3, 4, 5.
	fmt.Println(sum(nums) >= 15)
	//Output: true
}

func ExamplePipe_maxConcurrentProcessors() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var n int
	c := consumerFunc[int](func(_ context.Context) (int, error) {
		n += 1
		return n, nil
	})

	var processed atomic.Int64
	var mu sync.Mutex
	var nums []int
	p := processorFunc[int](func(_ context.Context, n int) error {
		processed.Add(1)
		if processed.Load() > 5 {
			cancel()
			return nil
		}
		// items are processed concurrently so we can print them here
		// since the order is not gauranteed
		mu.Lock()
		nums = append(nums, n)
		mu.Unlock()
		return nil
	})

	rt := Pipe[int](
		c,
		p,
		LogHandler(slog.Default().Handler()),
		MaxConcurrentProcessors(1),
	)

	err := rt.Run(ctx)
	if err != nil {
		fmt.Println(err)
		return
	}

	// Since there's only 1 processor goroutine and the
	// nums are consumed sequentially the nums slice
	// should be gauranteed to be 1 thru 5.
	slices.Sort(nums)
	fmt.Println(nums)
	//Output: [1 2 3 4 5]
}

func sum[T int | float64](xs []T) T {
	var total T = 0
	for _, x := range xs {
		total += x
	}
	return total
}
