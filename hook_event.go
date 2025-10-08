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

// Resolver defines a common interface for a Hook event (see [Event]).
type Resolver interface {
	// Next triggers the next handler in the hook's chain (if any).
	Next() error

	nextFunc() func() error
	setNextFunc(f func() error)
}

var _ Resolver = (*Event)(nil)

// Event implements [Resolver] and it is intended to be used as a base
// Hook event that you can embed in your custom typed event structs.
//
// Example:
//
//	type CustomEvent struct {
//		hook.Event
//
//		SomeField int
//	}
type Event struct {
	next func() error
}

// Next calls the next hook handler.
func (e *Event) Next() error {
	if e.next != nil {
		return e.next()
	}
	return nil
}

// nextFunc returns the function that Next calls.
func (e *Event) nextFunc() func() error {
	return e.next
}

// setNextFunc sets the function that Next calls.
func (e *Event) setNextFunc(f func() error) {
	e.next = f
}
