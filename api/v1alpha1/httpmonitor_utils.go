/*
Copyright 2020 Raising the Floor - International

Licensed under the New BSD license. You may not use this file except in
compliance with this License.

You may obtain a copy of the License at
https://github.com/GPII/universal/blob/master/LICENSE.txt

The R&D leading to these results received funding from the:
* Rehabilitation Services Administration, US Dept. of Education under
  grant H421A150006 (APCP)
* National Institute on Disability, Independent Living, and
  Rehabilitation Research (NIDILRR)
* Administration for Independent Living & Dept. of Education under grants
  H133E080022 (RERC-IT) and H133E130028/90RE5003-01-00 (UIITA-RERC)
* European Union's Seventh Framework Programme (FP7/2007-2013) grant
  agreement nos. 289016 (Cloud4all) and 610510 (Prosperity4All)
* William and Flora Hewlett Foundation
* Ontario Ministry of Research and Innovation
* Canadian Foundation for Innovation
* Adobe Foundation
* Consumer Electronics Association Foundation
*/
package v1alpha1

import (
	"context"
	"errors"
	"fmt"
	"github.com/oregondesignservices/monitoring-controller/httpclient"
	"net/http"
	"net/url"
	ctrl "sigs.k8s.io/controller-runtime"
	"strings"
	"sync"
	"time"
)

func replaceQueryParams(v map[string][]string, replacer *strings.Replacer) url.Values {
	if len(v) == 0 {
		return v
	}
	newValues := make(url.Values)

	for key, values := range v {
		for _, v := range values {
			newValues.Add(key, replacer.Replace(v))
		}
	}

	return newValues
}

func replaceHeader(v http.Header, replacer *strings.Replacer) http.Header {
	if len(v) == 0 {
		return v
	}

	newHeaders := make(http.Header)

	for key, values := range v {
		for _, v := range values {
			newHeaders.Add(key, replacer.Replace(v))
		}
	}

	return newHeaders
}

func (r *HttpRequest) BuildRequest() (*http.Request, error) {
	replacer := r.availableVariables.newReplacer()

	finalUrl := replacer.Replace(r.Url)
	body := replacer.Replace(r.Body)
	query := replaceQueryParams(r.QueryParams, replacer)
	header := replaceHeader(r.Headers, replacer)

	req, err := http.NewRequest(r.Method, finalUrl, strings.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header = header

	req.URL.RawQuery = query.Encode()
	return req, nil
}

func containsInt(needle int, haystay []int) bool {
	for _, val := range haystay {
		if val == needle {
			return true
		}
	}
	return false
}

// Send the HTTP request and parse any variables
func (r *HttpRequest) Do(client *http.Client) error {
	req, err := r.BuildRequest()
	if err != nil {
		return err
	}

	timeoutDuration, err := time.ParseDuration(r.Timeout)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutDuration)
	defer cancel()

	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	return r.handleResponse(resp)
}

func (r *HttpRequest) handleResponse(resp *http.Response) error {
	if resp == nil {
		return errors.New("got nil response object")
	}
	if !containsInt(resp.StatusCode, r.ExpectedResponseCodes) {
		return fmt.Errorf("not an expected error code: %d is not in %x", resp.StatusCode, r.ExpectedResponseCodes)
	}
	// Nothing to parse
	if len(r.VariablesFromResponse) == 0 {
		return nil
	}

	for _, variable := range r.VariablesFromResponse {
		err := variable.ParseFromResponse(resp)
		if err != nil {
			return err
		}
	}

	return nil
}

func (h *HttpMonitor) executeRequests() {
	client := httpclient.GetClient()

	// These variables are available for all requests to use
	availableVariables := VariableList{
		&Variable{
			Name:  "random-8",
			From:  FromTypeProvided,
			Value: "12345678", // @TODO make random
		},
	}
	for key, val := range h.Spec.Variables {
		availableVariables = append(availableVariables, &Variable{
			Name:  key,
			From:  FromTypeProvided,
			Value: val,
		})
	}

	// run requests
	for _, httpRequest := range h.Spec.Requests {
		entry := h.logger.WithValues("name", httpRequest.Name)
		entry.V(2).Info("executing request")
		httpRequest.VariablesFromResponse.clearValues()
		httpRequest.availableVariables = availableVariables

		err := httpRequest.Do(client)
		if err != nil {
			entry.Error(err, "failed to complete request", "name", httpRequest.Name)
			break
		}
		if len(httpRequest.VariablesFromResponse) > 0 {
			availableVariables = append(availableVariables, httpRequest.VariablesFromResponse...)
		}
	}

	// run cleanup
	for _, httpRequest := range h.Spec.Cleanup {
		entry := h.logger.WithValues("name", httpRequest.Name)
		entry.V(2).Info("executing cleanup request")
		httpRequest.VariablesFromResponse.clearValues()
		httpRequest.availableVariables = availableVariables

		err := httpRequest.Do(client)
		if err != nil {
			entry.Error(err, "failed to complete cleanup request", "name", httpRequest.Name)
		}
	}
}

func (h *HttpMonitor) Start() {
	if h.ticker != nil {
		panic("tried to start an already started HttpMonitor")
	}

	h.logger = ctrl.Log.
		WithName("httpmonitor").
		WithName("runner").
		WithValues("namespace", h.Namespace, "name", h.Name)

	h.ticker = time.NewTicker(h.Spec.Period.Duration)
	h.stopped = &sync.WaitGroup{}
	h.stopped.Add(1)
	go func() {
		defer h.stopped.Done()
		for _ = range h.ticker.C {
			h.executeRequests()
		}
	}()
}

func (h *HttpMonitor) Stop() {
	h.ticker.Stop()
	h.stopped.Wait()
}
