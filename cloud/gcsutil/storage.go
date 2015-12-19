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

// Package gcsutil provides tools for accessing Google Cloud Storage until they can be
// completely replaced by google.golang.org/cloud/storage.
package gcsutil

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const gsAccessURL = "https://storage.googleapis.com"

// TODO(mpl): doc. tell users to look elsewhere on how to set up an authenticated HTTP client.
type Client http.Client

// An Object holds the name of an object (its bucket and key) within
// Google Cloud Storage.
type Object struct {
	Bucket string
	Key    string
}

func (o *Object) valid() error {
	if o == nil {
		return errors.New("invalid nil Object")
	}
	if o.Bucket == "" {
		return errors.New("missing required Bucket field in Object")
	}
	if o.Key == "" {
		return errors.New("missing required Key field in Object")
	}
	return nil
}

// A SizedObject holds the bucket, key, and size of an object.
type SizedObject struct {
	Object
	Size int64
}

func (o *Object) String() string {
	if o == nil {
		return "<nil *Object>"
	}
	return fmt.Sprintf("%v/%v", o.Bucket, o.Key)
}

func (so SizedObject) String() string {
	return fmt.Sprintf("%v/%v (%vB)", so.Bucket, so.Key, so.Size)
}

// Makes a simple body-less google storage request
func (c *Client) simpleRequest(method, url_ string) (*http.Request, error) {
	req, err := http.NewRequest(method, url_, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-version", "2")
	return req, err
}

var ErrInvalidRange = fmt.Errorf("%d", http.StatusRequestedRangeNotSatisfiable)


// TODO(mpl): maybe move GetPartialObject code back into
// pkg/blobserver/google/cloudstorage's SubFetch. Still on the fence about it.

// GetPartialObject fetches part of a Google Cloud Storage object.
// If length is negative, the rest of the object is returned.
// It returns ErrInvalidRange if the server replies with http.StatusRequestedRangeNotSatisfiable.
// The caller must close rc.
func (c *Client) GetPartialObject(obj Object, offset, length int64) (rc io.ReadCloser, err error) {
	if offset < 0 {
		return nil, errors.New("invalid negative offset")
	}
	if err = obj.valid(); err != nil {
		return
	}

	req, err := c.simpleRequest("GET", gsAccessURL+"/"+obj.Bucket+"/"+obj.Key)
	if err != nil {
		return
	}
	if length >= 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	} else {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := (*http.Client)(c).Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET (offset=%d, length=%d) failed: %v\n", offset, length, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, os.ErrNotExist
	}
	if !(resp.StatusCode == http.StatusPartialContent || (offset == 0 && resp.StatusCode == http.StatusOK)) {
		resp.Body.Close()
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			return nil, ErrInvalidRange
		}
		return nil, fmt.Errorf("GET (offset=%d, length=%d) got failed status: %v\n", offset, length, resp.Status)
	}

	return resp.Body, nil
}

// EnumerateObjects lists the objects in a bucket.
// If after is non-empty, listing will begin with lexically greater object names.
// If limit is non-zero, the length of the list will be limited to that number.
func (c *Client) EnumerateObjects(bucket, after string, limit int) ([]SizedObject, error) {
	// Build url, with query params
	var params []string
	if after != "" {
		params = append(params, "marker="+url.QueryEscape(after))
	}
	if limit > 0 {
		params = append(params, fmt.Sprintf("max-keys=%v", limit))
	}
	query := ""
	if len(params) > 0 {
		query = "?" + strings.Join(params, "&")
	}

	req, err := c.simpleRequest("GET", gsAccessURL+"/"+bucket+"/"+query)
	if err != nil {
		return nil, err
	}
	resp, err := (*http.Client)(c).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Bad enumerate response code: %v", resp.Status)
	}

	var xres struct {
		Contents []SizedObject
	}
	defer resp.Body.Close()
	if err = xml.NewDecoder(resp.Body).Decode(&xres); err != nil {
		return nil, err
	}

	// Fill in the Bucket on all the SizedObjects
	for _, o := range xres.Contents {
		o.Bucket = bucket
	}

	return xres.Contents, nil
}

