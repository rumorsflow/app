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

import (
	"errors"
	"testing"
)

func TestHookAddHandlerAndAdd(t *testing.T) {
	calls := ""

	h := Hook[*Event]{}

	h.BindFunc(func(e *Event) error { calls += "1"; return e.Next() })
	h.BindFunc(func(e *Event) error { calls += "2"; return e.Next() })
	h3Id := h.BindFunc(func(e *Event) error { calls += "3"; return e.Next() })
	h.Bind(&Handler[*Event]{
		ID:   h3Id, // should replace 3
		Func: func(e *Event) error { calls += "3'"; return e.Next() },
	})
	h.Bind(&Handler[*Event]{
		Func:     func(e *Event) error { calls += "4"; return e.Next() },
		Priority: -2,
	})
	h.Bind(&Handler[*Event]{
		Func:     func(e *Event) error { calls += "5"; return e.Next() },
		Priority: -1,
	})
	h.Bind(&Handler[*Event]{
		Func: func(e *Event) error { calls += "6"; return e.Next() },
	})
	h.Bind(&Handler[*Event]{
		Func: func(e *Event) error { calls += "7"; _ = e.Next(); return errors.New("test") }, // error shouldn't stop the chain
	})

	_ = h.Trigger(
		&Event{},
		func(e *Event) error { calls += "8"; return e.Next() },
		func(e *Event) error { calls += "9"; return nil }, // skip next
		func(e *Event) error { calls += "10"; return e.Next() },
	)

	if total := len(h.handlers); total != 7 {
		t.Fatalf("Expected %d handlers, found %d", 7, total)
	}

	expectedCalls := "45123'6789"

	if calls != expectedCalls {
		t.Fatalf("Expected calls sequence %q, got %q", expectedCalls, calls)
	}
}

func TestHookLength(t *testing.T) {
	h := Hook[*Event]{}

	if l := h.Length(); l != 0 {
		t.Fatalf("Expected 0 hook handlers, got %d", l)
	}

	h.BindFunc(func(e *Event) error { return e.Next() })
	h.BindFunc(func(e *Event) error { return e.Next() })

	if l := h.Length(); l != 2 {
		t.Fatalf("Expected 2 hook handlers, got %d", l)
	}
}

func TestHookUnbind(t *testing.T) {
	h := Hook[*Event]{}

	calls := ""

	id0 := h.BindFunc(func(e *Event) error { calls += "0"; return e.Next() })
	id1 := h.BindFunc(func(e *Event) error { calls += "1"; return e.Next() })
	h.BindFunc(func(e *Event) error { calls += "2"; return e.Next() })
	h.Bind(&Handler[*Event]{
		Func: func(e *Event) error { calls += "3"; return e.Next() },
	})

	h.Unbind("missing") // should do nothing and not panic

	if total := len(h.handlers); total != 4 {
		t.Fatalf("Expected %d handlers, got %d", 4, total)
	}

	h.Unbind(id1, id0)

	if total := len(h.handlers); total != 2 {
		t.Fatalf("Expected %d handlers, got %d", 2, total)
	}

	err := h.Trigger(&Event{}, func(e *Event) error { calls += "4"; return e.Next() })
	if err != nil {
		t.Fatal(err)
	}

	expectedCalls := "234"

	if calls != expectedCalls {
		t.Fatalf("Expected calls sequence %q, got %q", expectedCalls, calls)
	}
}

func TestHookUnbindAll(t *testing.T) {
	h := Hook[*Event]{}

	h.UnbindAll() // should do nothing and not panic

	h.BindFunc(func(e *Event) error { return nil })
	h.BindFunc(func(e *Event) error { return nil })

	if total := len(h.handlers); total != 2 {
		t.Fatalf("Expected %d handlers before UnbindAll, found %d", 2, total)
	}

	h.UnbindAll()

	if total := len(h.handlers); total != 0 {
		t.Fatalf("Expected no handlers after UnbindAll, found %d", total)
	}
}

func TestHookTriggerErrorPropagation(t *testing.T) {
	err := errors.New("test")

	scenarios := []struct {
		name          string
		handlers      []func(*Event) error
		expectedError error
	}{
		{
			"without error",
			[]func(*Event) error{
				func(e *Event) error { return e.Next() },
				func(e *Event) error { return e.Next() },
			},
			nil,
		},
		{
			"with error",
			[]func(*Event) error{
				func(e *Event) error { return e.Next() },
				func(e *Event) error { _ = e.Next(); return err },
				func(e *Event) error { return e.Next() },
			},
			err,
		},
	}

	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			h := Hook[*Event]{}
			for _, handler := range s.handlers {
				h.BindFunc(handler)
			}
			result := h.Trigger(&Event{})
			if !errors.Is(result, s.expectedError) {
				t.Fatalf("Expected %v, got %v", s.expectedError, result)
			}
		})
	}
}
