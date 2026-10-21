// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sinkconfig

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

// WatchNamedResourceOptions specifies the operations required to watch a single named Kubernetes resource.
type WatchNamedResourceOptions struct {
	Get     func(ctx context.Context) (metav1.Object, error)
	Watch   func(ctx context.Context, rv string) (watch.Interface, error)
	OnEvent func(eventType watch.EventType, obj runtime.Object)
}

// WatchNamedResource watches a single named resource using a low-privilege get+watch loop.
// It handles watch bookmarks, reconnects with exponential backoff on watch setup errors,
// and recovers from HTTP 410 (Gone / too old resource version) without tight loops by stopping the watcher,
// sleeping 500ms, and re-fetching the resource to refresh its ResourceVersion.
func WatchNamedResource(ctx context.Context, opts WatchNamedResourceOptions) error {
	var rv string

	obj, err := opts.Get(ctx)
	if err == nil {
		rv = obj.GetResourceVersion()
		if ro, ok := obj.(runtime.Object); ok && opts.OnEvent != nil {
			opts.OnEvent(watch.Added, ro)
		}
	} else if apierrors.IsNotFound(err) {
		rv = ""
		if opts.OnEvent != nil {
			opts.OnEvent(watch.Deleted, nil)
		}
	} else if ctx.Err() != nil {
		return ctx.Err()
	}

	backoff := 1 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		watcher, err := opts.Watch(ctx, rv)
		if err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			obj, err := opts.Get(ctx)
			if err == nil {
				rv = obj.GetResourceVersion()
				if ro, ok := obj.(runtime.Object); ok && opts.OnEvent != nil {
					opts.OnEvent(watch.Modified, ro)
				}
			} else if apierrors.IsNotFound(err) {
				rv = ""
				if opts.OnEvent != nil {
					opts.OnEvent(watch.Deleted, nil)
				}
			}
			continue
		}

		backoff = 1 * time.Second

		stopped := false
		for !stopped {
			select {
			case <-ctx.Done():
				watcher.Stop()
				return ctx.Err()
			case event, ok := <-watcher.ResultChan():
				if !ok {
					stopped = true
					break
				}
				switch event.Type {
				case watch.Added, watch.Modified:
					if meta, ok := event.Object.(metav1.Object); ok {
						if meta.GetResourceVersion() != "" && meta.GetResourceVersion() == rv {
							continue
						}
						rv = meta.GetResourceVersion()
						if opts.OnEvent != nil {
							opts.OnEvent(event.Type, event.Object)
						}
					}
				case watch.Deleted:
					rv = ""
					if opts.OnEvent != nil {
						opts.OnEvent(watch.Deleted, event.Object)
					}
				case watch.Bookmark:
					if meta, ok := event.Object.(metav1.Object); ok {
						rv = meta.GetResourceVersion()
					}
				case watch.Error:
					stopped = true
					break
				}
			}
		}
		watcher.Stop()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}

		obj, err := opts.Get(ctx)
		if err == nil {
			rv = obj.GetResourceVersion()
			if ro, ok := obj.(runtime.Object); ok && opts.OnEvent != nil {
				opts.OnEvent(watch.Modified, ro)
			}
		} else if apierrors.IsNotFound(err) {
			rv = ""
			if opts.OnEvent != nil {
				opts.OnEvent(watch.Deleted, nil)
			}
		}
	}
}
