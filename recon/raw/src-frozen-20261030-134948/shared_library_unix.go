//go:build cgo && (linux || darwin || freebsd)

package main

/*
#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

extern int ModelMetaSyncPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);

static const char* model_meta_sync_shared_object_path() {
	Dl_info info;
	if (dladdr((void*)&ModelMetaSyncPluginCall, &info) == 0 || info.dli_fname == NULL) {
		return NULL;
	}
	return info.dli_fname;
}
*/
import "C"

func sharedLibraryPath() string {
	sharedObjectPath := C.model_meta_sync_shared_object_path()
	if sharedObjectPath == nil {
		return ""
	}
	return C.GoString(sharedObjectPath)
}
