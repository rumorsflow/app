package app

// Copied from github.com/pocketbase/pocketbase to avoid nuances around the specific
//
// -------------------------------------------------------------------
// The MIT License (MIT) Copyright (c) 2022 - present, Gani Georgiev
// Permission is hereby granted, free of charge, to any person obtaining a copy of this
// software and associated documentation files (the "Software"), to deal in the Software
// without restriction, including without limitation the rights to use, copy, modify,
// merge, publish, distribute, sublicense, and/or sell copies of the Software, and to
// permit persons to whom the Software is furnished to do so, subject to the following
// conditions:
// The above copyright notice and this permission notice shall be included in all copies
// or substantial portions of the Software.
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED,
// INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR
// PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE
// LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT
// OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER
// DEALINGS IN THE SOFTWARE.
// -------------------------------------------------------------------

import "slices"

// Tagger defines an interface for event data structs that support tags/groups/categories/etc.
// Usually used together with TaggedHook.
type Tagger interface {
	Resolver

	Tags() []string
}

// wrapped local Hook embedded struct to limit the public API surface.
type mainHook[T Tagger] struct {
	*Hook[T]
}

// NewTaggedHook creates a new TaggedHook with the provided main hook and optional tags.
func NewTaggedHook[T Tagger](hook *Hook[T], tags ...string) *TaggedHook[T] {
	return &TaggedHook[T]{
		mainHook[T]{hook},
		tags,
	}
}

// TaggedHook defines a proxy hook which register handlers that are triggered only
// if the TaggedHook.tags are empty or includes at least one of the event data tag(s).
type TaggedHook[T Tagger] struct {
	mainHook[T]

	tags []string
}

// CanTriggerOn checks if the current TaggedHook can be triggered with
// the provided event data tags.
//
// It returns always true if the hook doens't have any tags.
func (h *TaggedHook[T]) CanTriggerOn(tagsToCheck []string) bool {
	if len(h.tags) == 0 {
		return true // match all
	}

	for _, t := range tagsToCheck {
		if slices.Contains(h.tags, t) {
			return true
		}
	}

	return false
}

// Bind registers the provided handler to the current hooks queue.
//
// It is similar to [Hook.Bind] with the difference that the handler
// function is invoked only if the event data tags satisfy h.CanTriggerOn.
func (h *TaggedHook[T]) Bind(handler *Handler[T]) string {
	fn := handler.Func

	handler.Func = func(e T) error {
		if h.CanTriggerOn(e.Tags()) {
			return fn(e)
		}

		return e.Next()
	}

	return h.mainHook.Bind(handler)
}

// BindFunc registers a new handler with the specified function.
//
// It is similar to [Hook.Bind] with the difference that the handler
// function is invoked only if the event data tags satisfy h.CanTriggerOn.
func (h *TaggedHook[T]) BindFunc(fn func(e T) error) string {
	return h.mainHook.BindFunc(func(e T) error {
		if h.CanTriggerOn(e.Tags()) {
			return fn(e)
		}

		return e.Next()
	})
}
