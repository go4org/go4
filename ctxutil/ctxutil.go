/*
Copyright 2015 The Go4 Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package ctxutil contains golang.org/x/net/context related utilities.
package ctxutil

import (
	"net/http"

	"golang.org/x/net/context"
	"golang.org/x/oauth2"
)

// Client returns the client that was registered in the ctx as oauth2.HTTPClient, if any,
// http.DefaultClient otherwise.
func Client(ctx context.Context) *http.Client {
	if ctx != nil {
		if hc, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok {
			return hc
		}
	}
	return http.DefaultClient
}
