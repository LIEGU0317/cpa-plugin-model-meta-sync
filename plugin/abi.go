package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int ModelMetaSyncPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void ModelMetaSyncPluginFree(void*, size_t);
extern void ModelMetaSyncPluginShutdown(void);
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const maxCGoBytesLen = C.size_t(1<<31 - 1)

var abiState = struct {
	sync.RWMutex
	host         *C.cliproxy_host_api
	plugin       *pluginState
	shuttingDown bool
	inFlight     sync.WaitGroup
}{}

type abiEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *abiError       `json:"error,omitempty"`
}

type abiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type abiLifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
	PluginDir  string `json:"plugin_dir,omitempty"`
}

type abiRegistration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  abiCapabilities    `json:"capabilities"`
}

type abiCapabilities struct {
	ManagementAPI bool `json:"management_api"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil {
		return 1
	}
	abiState.Lock()
	abiState.host = host
	abiState.shuttingDown = false
	abiState.Unlock()

	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.ModelMetaSyncPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.ModelMetaSyncPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.ModelMetaSyncPluginShutdown)
	return 0
}

//export ModelMetaSyncPluginCall
func ModelMetaSyncPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeABIResponse(response, abiErrorEnvelope("invalid_method", "method is required"))
		return 0
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		if requestLen > maxCGoBytesLen {
			writeABIResponse(response, abiErrorEnvelope("request_too_large", "request payload is too large"))
			return 0
		}
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleABIMethod(context.Background(), C.GoString(method), requestBytes)
	if errHandle != nil {
		writeABIResponse(response, abiErrorEnvelope("plugin_error", errHandle.Error()))
		return 0
	}
	writeABIResponse(response, raw)
	return 0
}

//export ModelMetaSyncPluginFree
func ModelMetaSyncPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export ModelMetaSyncPluginShutdown
func ModelMetaSyncPluginShutdown() {
	abiState.Lock()
	abiState.shuttingDown = true
	abiState.plugin = nil
	abiState.host = nil
	abiState.Unlock()
	abiState.inFlight.Wait()
	stopBackground()
}

func handleABIMethod(ctx context.Context, method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return handleRegister(request)
	}

	p, done, errPlugin := beginPluginCall()
	if errPlugin != nil {
		return nil, errPlugin
	}
	defer done()

	switch method {
	case pluginabi.MethodManagementRegister:
		var req pluginapi.ManagementRegistrationRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errCall := p.RegisterManagement(ctx, req)
		if errCall != nil {
			return nil, errCall
		}
		// Handlers are interfaces and cannot cross the JSON ABI; the host attaches
		// its own adapter after decoding the route descriptors.
		out := abiManagementRegistration{
			Routes:    make([]abiManagementRoute, 0, len(resp.Routes)),
			Resources: make([]abiManagementRoute, 0, len(resp.Resources)),
		}
		for _, route := range resp.Routes {
			out.Routes = append(out.Routes, abiManagementRoute{
				Method:      route.Method,
				Path:        route.Path,
				Menu:        route.Menu,
				Description: route.Description,
			})
		}
		for _, route := range resp.Resources {
			out.Resources = append(out.Resources, abiManagementRoute{
				Path:        route.Path,
				Menu:        route.Menu,
				Description: route.Description,
			})
		}
		return abiOKEnvelope(out)
	case pluginabi.MethodManagementHandle:
		var req pluginapi.ManagementRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errCall := p.HandleManagement(ctx, req)
		if errCall != nil {
			return nil, errCall
		}
		return abiOKEnvelope(resp)
	case pluginabi.MethodRequestComplete:
		// Not a capability of this plugin; acknowledge defensively.
		return abiOKEnvelope(struct{}{})
	default:
		return abiErrorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func handleRegister(request []byte) ([]byte, error) {
	var req abiLifecycleRequest
	if errDecode := json.Unmarshal(request, &req); errDecode != nil {
		return nil, errDecode
	}
	p, errBuild := buildPlugin(req.ConfigYAML, req.PluginDir)
	if errBuild != nil {
		return nil, errBuild
	}
	abiState.Lock()
	abiState.plugin = p
	abiState.shuttingDown = false
	abiState.Unlock()
	return abiOKEnvelope(abiRegistration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata:      p.metadata(),
		Capabilities: abiCapabilities{
			ManagementAPI: true,
		},
	})
}

func beginPluginCall() (*pluginState, func(), error) {
	abiState.Lock()
	defer abiState.Unlock()
	if abiState.shuttingDown {
		return nil, nil, fmt.Errorf("model-meta-sync plugin is shutting down")
	}
	if abiState.plugin == nil {
		return nil, nil, fmt.Errorf("model-meta-sync plugin is not registered")
	}
	abiState.inFlight.Add(1)
	return abiState.plugin, abiState.inFlight.Done, nil
}

func abiOKEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(abiEnvelope{OK: true, Result: raw})
}

func abiErrorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(abiEnvelope{OK: false, Error: &abiError{Code: code, Message: message}})
	return raw
}

func writeABIResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

type abiManagementRoute struct {
	Method      string `json:"method,omitempty"`
	Path        string `json:"path,omitempty"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

type abiManagementRegistration struct {
	Routes    []abiManagementRoute `json:"routes,omitempty"`
	Resources []abiManagementRoute `json:"resources,omitempty"`
}
