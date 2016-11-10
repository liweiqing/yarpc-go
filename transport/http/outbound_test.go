// Copyright (c) 2016 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package http

import (
	"bytes"
	"context"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"go.uber.org/yarpc/encoding/raw"
	"go.uber.org/yarpc/transport"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCallSuccess(t *testing.T) {
	successServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, req *http.Request) {
			defer req.Body.Close()

			ttl := req.Header.Get(TTLMSHeader)
			ttlms, err := strconv.Atoi(ttl)
			assert.NoError(t, err, "can parse TTL header")
			assert.InDelta(t, ttlms, 1000, 5, "ttl header within tolerance")

			assert.Equal(t, "caller", req.Header.Get(CallerHeader))
			assert.Equal(t, "service", req.Header.Get(ServiceHeader))
			assert.Equal(t, "raw", req.Header.Get(EncodingHeader))
			assert.Equal(t, "hello", req.Header.Get(ProcedureHeader))

			body, err := ioutil.ReadAll(req.Body)
			if assert.NoError(t, err) {
				assert.Equal(t, []byte("world"), body)
			}

			w.Header().Set("rpc-header-foo", "bar")
			_, err = w.Write([]byte("great success"))
			assert.NoError(t, err)
		},
	))
	defer successServer.Close()

	out := NewOutbound(successServer.URL)
	require.NoError(t, out.Start(transport.NoDeps), "failed to start outbound")
	defer out.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := out.Call(ctx, &transport.Request{
		Caller:    "caller",
		Service:   "service",
		Encoding:  raw.Encoding,
		Procedure: "hello",
		Body:      bytes.NewReader([]byte("world")),
	})
	require.NoError(t, err)
	defer res.Body.Close()

	foo, ok := res.Headers.Get("foo")
	assert.True(t, ok, "value for foo expected")
	assert.Equal(t, "bar", foo, "foo value mismatch")

	body, err := ioutil.ReadAll(res.Body)
	if assert.NoError(t, err) {
		assert.Equal(t, []byte("great success"), body)
	}
}

func TestOutboundHeaders(t *testing.T) {
	tests := []struct {
		desc    string
		context context.Context
		headers transport.Headers

		wantHeaders map[string]string
	}{
		{
			desc:    "application headers",
			headers: transport.NewHeaders().With("foo", "bar").With("baz", "Qux"),
			wantHeaders: map[string]string{
				"Rpc-Header-Foo": "bar",
				"Rpc-Header-Baz": "Qux",
			},
		},
	}

	for _, tt := range tests {
		server := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				defer r.Body.Close()
				for k, v := range tt.wantHeaders {
					assert.Equal(
						t, v, r.Header.Get(k), "%v: header %v did not match", tt.desc, k)
				}
			},
		))
		defer server.Close()

		ctx := tt.context
		if ctx == nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			defer cancel()
		}

		out := NewOutbound(server.URL)
		require.NoError(t, out.Start(transport.NoDeps), "failed to start outbound")
		defer out.Stop()

		res, err := out.Call(ctx, &transport.Request{
			Caller:    "caller",
			Service:   "service",
			Encoding:  raw.Encoding,
			Headers:   tt.headers,
			Procedure: "hello",
			Body:      bytes.NewReader([]byte("world")),
		})

		if !assert.NoError(t, err, "%v: call failed", tt.desc) {
			continue
		}

		if !assert.NoError(t, res.Body.Close(), "%v: failed to close response body") {
			continue
		}
	}
}

func TestCallFailures(t *testing.T) {
	notFoundServer := httptest.NewServer(http.NotFoundHandler())
	defer notFoundServer.Close()

	internalErrorServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "great sadness", http.StatusInternalServerError)
		}))
	defer internalErrorServer.Close()

	tests := []struct {
		url      string
		messages []string
	}{
		{"not a URL", []string{"protocol scheme"}},
		{notFoundServer.URL, []string{"404", "page not found"}},
		{internalErrorServer.URL, []string{"great sadness"}},
	}

	for _, tt := range tests {
		out := NewOutbound(tt.url)
		require.NoError(t, out.Start(transport.NoDeps), "failed to start outbound")
		defer out.Stop()

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := out.Call(ctx, &transport.Request{
			Caller:    "caller",
			Service:   "service",
			Encoding:  raw.Encoding,
			Procedure: "wat",
			Body:      bytes.NewReader([]byte("huh")),
		})
		assert.Error(t, err, "expected failure")
		for _, msg := range tt.messages {
			assert.Contains(t, err.Error(), msg)
		}
	}
}

func TestStartTwice(t *testing.T) {
	out := NewOutbound("http://localhost:9999")
	if assert.NoError(t, out.Start(transport.NoDeps)) {
		err := out.Start(transport.NoDeps)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "http.Outbound has already been started")
	}
}

func TestStopWithoutStarting(t *testing.T) {
	out := NewOutbound("http://localhost:9999")
	err := out.Stop()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "http.Outbound has not been started")
}

func TestCallWithoutStarting(t *testing.T) {
	out := NewOutbound("http://localhost:9999")
	assert.Panics(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		out.Call(
			ctx,
			&transport.Request{
				Caller:    "caller",
				Service:   "service",
				Encoding:  raw.Encoding,
				Procedure: "foo",
				Body:      bytes.NewReader([]byte("sup")),
			},
		)
	})
}
