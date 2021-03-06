// Based on Syscall event parsing developed by Iago López Galeiras
// The metagenerator now also supports generation of network events

package metagenerator

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

const syscallsPath = `/sys/kernel/debug/tracing/events/syscalls/`

const headers = `
// Generated file, do not edit.
// Source: metagenerator.go

`
const headersC = `
#include "../bpf/events-struct.h"
`

// TODO: make slice sizes fixed so we can decode it,
// for now it's fine since we're not using any of these yet.
const kernelStructs = headers + `
package tracer

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

import "C"

type FdInfo struct {
	Path  string
	Ino   uint64
	Major uint64
	Minor uint64
}

// Pid -> Fd -> FdInfo
type FdMap struct {
	sync.RWMutex
	items map[uint32]map[uint32]FdInfo
}

func NewFdMap() *FdMap {
	return &FdMap{
		items: make(map[uint32]map[uint32]FdInfo),
	}
}

func (f *FdMap) Get(pid, fd uint32) (*FdInfo, bool) {
	f.RLock()
	defer f.RUnlock()

	inner, ok := f.items[pid]
	if !ok {
		return nil, ok
	}

	info, ok := inner[fd]
	return &info, ok
}

func (f *FdMap) Put(pid, fd uint32, info FdInfo) {
	f.Lock()
	defer f.Unlock()

	if _, ok := f.items[pid]; !ok {
		f.items[pid] = make(map[uint32]FdInfo)
	}

	f.items[pid][fd] = info
}

func (f *FdMap) Delete(pid, fd uint32) {
	f.Lock()
	defer f.Unlock()

	if m, ok := f.items[pid]; ok {
		delete(m, fd)
	}
}

func (f *FdMap) DeletePid(pid uint32) {
	f.Lock()
	defer f.Unlock()

	delete(f.items, pid)
}

func (f *FdMap) Clear() {
	f.Lock()
	defer f.Unlock()

	f.items = make(map[uint32]map[uint32]FdInfo)
}

type Context struct {
	Fds *FdMap
}

// kernel structures

type CapUserHeader struct {
	Version uint32
	Pid     int64
}

type CapUserData struct {
	Effective   uint32
	Permitted   uint32
	Inheritable uint32
}

type SigSet struct {
	Sig []uint64
}

type Stack struct {
	SsSp    []byte // size?
	SsFlags int64
	SsSize  int64
}

type Itimerspec struct {
	ItInterval syscall.Timespec
	ItValue    syscall.Timespec
}

type MqAttr struct {
	MqFlags   int64
	MqMaxmsg  int64
	MqMsgsize int64
	MqCurmsgs int64
	Reserved  [4]int64
}

type Sigaction struct {
	SaHandler  unsafe.Pointer
	SaRestorer unsafe.Pointer
	SaMask     SigSet
}

type Sigval struct {
	SivalInt int64
	SivalPtr unsafe.Pointer
}

type Sigevent struct {
	SigevValue  Sigval
	SigevSigno  int64
	SigevNotify int64
	Pad         [13]int // (64 - 3*4) / 4
}

type FileHandle struct {
	HandleBytes uint32
	HandleType  int64
	FHandle     []uint8
}

type GetCPUCache struct {
	Blob [16]uint64
}

type IoCb struct {
	AioData      uint64
	Padding      uint32
	AioLioOpcode uint16
	AioReqPrio   int16
	AioFilDes    uint32
	AioBuf       uint64
	AioNbytes    uint64
	AioOffset    int64
	AioReserved2 uint64
	AioFlags     uint32
	AioResfd     uint32
}

type IoEvent struct {
	Data uint64
	Obj  uint64
	Res  int64
	Res2 int64
}

type KexecSegment struct {
	Buf   unsafe.Pointer
	Bufsz int64
	Mem   unsafe.Pointer
	Memsz int64
}

type Msgbuf struct {
	Mtype int64
	Mtext [1]byte
}

type Pollfd struct {
	Fd      int64
	Events  int16
	Revents int16
}

type RobustList struct {
	Next unsafe.Pointer
}

type RobustListHead struct {
	List          RobustList
	FutexOffset   int64
	ListOpPending unsafe.Pointer
}

type SysctlArgs struct {
	Name    []int64
	Nlen    int64
	Oldval  unsafe.Pointer
	OldLenp int64
	Newval  unsafe.Pointer
	Newlen  int64
}

type Timezone struct {
	TzMinuteswest int64
	TzDsttime     int64
}

type BpfAttr struct {
	Data [48]byte
}

type UserMsghdr struct {
	MsgName       unsafe.Pointer
	MsgNamelen    int64
	MsgIov        syscall.Iovec
	MsgIovlen     int64
	MsgControl    unsafe.Pointer
	MsgControllen int64
	MsgFlags      uint64
}

func (e FileEvent) String(ret int64) string {
	return fmt.Sprintf("Fd %d ", e.Fd)
}

func (e FileEvent) GetArgN(n int, ret int64) (string, error) {
	return "", fmt.Errorf("FileEvent.GetArgN not implemented")
}

// FileEvent is not meant to be seen by the users
func (e FileEvent) Metric() *Metric {
	return nil
}

// syscall data
`

const maxBufferSize = 256

var (
	protoTypeConversions = map[string]string{
		"BpfAttr":            "todo",
		"CapUserData":        "todo",
		"CapUserHeader":      "todo",
		"FileHandle":         "todo",
		"GetCPUCache":        "todo",
		"IoCb":               "todo",
		"IoEvent":            "todo",
		"Itimerspec":         "todo",
		"KexecSegment":       "todo",
		"MqAttr":             "todo",
		"Msgbuf":             "todo",
		"Pollfd":             "todo",
		"RobustListHead":     "todo",
		"SigSet":             "todo",
		"Sigaction":          "todo",
		"Sigevent":           "todo",
		"Stack":              "todo",
		"SysctlArgs":         "todo",
		"Timezone":           "todo",
		"[256]byte":          "bytes",
		"[]IoCb":             "todo",
		"[]RobustListHead":   "todo",
		"int16":              "int32",
		"int32":              "int32",
		"int64":              "int64",
		"syscall.Dirent":     "todo",
		"syscall.EpollEvent": "todo",
		"syscall.FdSet":      "todo",
		"syscall.Iovec":      "todo",
		"syscall.Msghdr":     "todo",
		"syscall.Rlimit":     "todo",
		"syscall.Rusage":     "todo",
		"syscall.Sockaddr":   "todo",
		"syscall.Stat_t":     "todo",
		"syscall.Statfs_t":   "todo",
		"syscall.Sysinfo_t":  "todo",
		"syscall.Timespec":   "todo",
		"syscall.Timeval":    "todo",
		"syscall.Timex":      "todo",
		"syscall.Tms":        "todo",
		"syscall.Ustat_t":    "todo",
		"syscall.Utimbuf":    "todo",
		"syscall.Utsname":    "todo",
		"uint16":             "uint32",
		"uint32":             "uint32",
		"uint64":             "uint64",
		"unsafe.Pointer":     "todo",
	}

	goTypeConversions = map[string]string{
		"aio_context_t *":             "uint64",
		"aio_context_t":               "uint64",
		"cap_user_data_t":             "CapUserData",
		"cap_user_header_t":           "CapUserHeader",
		"char *":                      fmt.Sprintf("[%d]byte", maxBufferSize),
		"const cap_user_data_t":       "CapUserData",
		"const char *":                fmt.Sprintf("[%d]byte", maxBufferSize),
		"const clockid_t":             "uint32",
		"const int *":                 "int64",
		"const sigset_t *":            "SigSet",
		"const stack_t *":             "Stack",
		"const struct iovec *":        "syscall.Iovec",
		"const struct itimerspec *":   "Itimerspec",
		"const struct mq_attr *":      "MqAttr",
		"const struct rlimit64 *":     "syscall.Rlimit",
		"const struct sigaction *":    "Sigaction",
		"const struct sigevent *":     "Sigevent",
		"const struct timespec *":     "syscall.Timespec",
		"const unsigned long *":       "uint64",
		"const void * *":              "unsafe.Pointer",
		"const void *":                "unsafe.Pointer",
		"fd_set *":                    "syscall.FdSet",
		"gid_t *":                     "uint64",
		"gid_t":                       "uint32",
		"int *":                       "int64",
		"int":                         "int64",
		"key_serial_t":                "int32",
		"key_t":                       "int64",
		"loff_t *":                    "int64",
		"loff_t":                      "int64",
		"long":                        "int64",
		"mqd_t":                       "int64",
		"off_t":                       "int64",
		"pid_t":                       "int64",
		"qid_t":                       "uint32",
		"__s32":                       "uint32",
		"siginfo_t *":                 "unsafe.Pointer", // unknown
		"sigset_t *":                  "SigSet",
		"size_t *":                    "int64",
		"size_t":                      "int64",
		"stack_t *":                   "Stack",
		"struct epoll_event *":        "syscall.EpollEvent",
		"struct file_handle *":        "FileHandle",
		"struct getcpu_cache *":       "GetCPUCache",
		"struct iocb * *":             "IoCb",
		"struct iocb *":               "[]IoCb",
		"struct io_event *":           "IoEvent",
		"struct itimerspec *":         "Itimerspec",
		"struct itimerval *":          "Itimerspec",
		"struct kexec_segment *":      "KexecSegment",
		"struct linux_dirent64 *":     "syscall.Dirent",
		"struct linux_dirent *":       "syscall.Dirent",
		"struct mmsghdr *":            "syscall.Msghdr",
		"struct mq_attr *":            "MqAttr",
		"struct msgbuf *":             "Msgbuf",
		"struct msqid_ds *":           "unsafe.Pointer", // obsolete
		"struct new_utsname *":        "syscall.Utsname",
		"struct perf_event_attr *":    "unsafe.Pointer", // too big
		"struct pollfd *":             "Pollfd",
		"struct rlimit64 *":           "syscall.Rlimit",
		"struct rlimit *":             "syscall.Rlimit",
		"struct robust_list_head * *": "[]RobustListHead",
		"struct robust_list_head *":   "RobustListHead",
		"struct rusage *":             "syscall.Rusage",
		"struct sched_attr *":         "unsafe.Pointer", // unknown
		"struct sched_param *":        "unsafe.Pointer", // unknown
		"struct sembuf *":             "unsafe.Pointer", // unknown
		"struct shmid_ds *":           "unsafe.Pointer", // unknown
		"struct sigaction *":          "unsafe.Pointer", // unknown
		"struct sigevent *":           "unsafe.Pointer", // unknown
		"struct siginfo *":            "unsafe.Pointer", // unknown
		"struct sockaddr *":           "syscall.Sockaddr",
		"struct stat *":               "syscall.Stat_t",
		"struct statfs *":             "syscall.Statfs_t",
		"struct __sysctl_args *":      "SysctlArgs",
		"struct sysinfo *":            "syscall.Sysinfo_t",
		"struct timespec *":           "syscall.Timespec",
		"struct timeval *":            "syscall.Timeval",
		"struct timex *":              "syscall.Timex",
		"struct timezone *":           "Timezone",
		"struct tms *":                "syscall.Tms",
		"struct user_msghdr *":        "unsafe.Pointer", // unknown
		"struct ustat *":              "syscall.Ustat_t",
		"struct utimbuf *":            "syscall.Utimbuf",
		"timer_t *":                   "int64",
		"timer_t":                     "int64",
		"time_t *":                    "int64",
		"u32 *":                       "uint32",
		"u32":                         "uint32",
		"u64":                         "uint64",
		"__u64":                       "uint64",
		"uid_t *":                     "uint64",
		"uid_t":                       "uint32",
		"umode_t":                     "uint64",
		"union bpf_attr *":            "BpfAttr",
		"unsigned char *":             fmt.Sprintf("[%d]byte", maxBufferSize),
		"unsigned *":                  "uint64",
		"unsigned":                    "uint64",
		"unsigned int *":              "uint64",
		"unsigned int":                "uint64",
		"unsigned long *":             "uint64",
		"unsigned long":               "uint64",
		"void *":                      "unsafe.Pointer",
	}

	cTypeConversions = map[string]string{
		"aio_context_t *":             "u64",
		"aio_context_t":               "aio_context_t",
		"cap_user_data_t":             "cap_user_data_t",
		"cap_user_header_t":           "cap_user_header_t",
		"char *":                      "char",
		"const cap_user_data_t":       "cap_user_data_t",
		"const char *":                "char",
		"const clockid_t":             "u32",
		"const int *":                 "s64",
		"const sigset_t *":            "u64",
		"const stack_t *":             "u64",
		"const struct iovec *":        "u64",
		"const struct itimerspec *":   "u64",
		"const struct mq_attr *":      "u64",
		"const struct rlimit64 *":     "u64",
		"const struct sigaction *":    "u64",
		"const struct sigevent *":     "u64",
		"const struct timespec *":     "u64",
		"const unsigned long *":       "u64",
		"const void * *":              "u64",
		"const void *":                "u64",
		"fd_set *":                    "u64",
		"gid_t *":                     "u64",
		"gid_t":                       "gid_t",
		"int *":                       "u64",
		"int":                         "s64",
		"key_serial_t":                "key_serial_t",
		"key_t":                       "key_t",
		"loff_t *":                    "s64",
		"loff_t":                      "loff_t",
		"long":                        "s64",
		"mqd_t":                       "mdq_t",
		"off_t":                       "off_t",
		"pid_t":                       "pid_t",
		"qid_t":                       "qid_t",
		"__s32":                       "s32",
		"siginfo_t *":                 "u64",
		"sigset_t *":                  "u64",
		"size_t *":                    "u64",
		"size_t":                      "int64_t", // varies in kernel
		"stack_t *":                   "u64",
		"struct epoll_event *":        "u64",
		"struct file_handle *":        "u64",
		"struct getcpu_cache *":       "u64",
		"struct iocb * *":             "u64",
		"struct iocb *":               "u64",
		"struct io_event *":           "u64",
		"struct itimerspec *":         "u64",
		"struct itimerval *":          "u64",
		"struct kexec_segment *":      "u64",
		"struct linux_dirent64 *":     "u64",
		"struct linux_dirent *":       "u64",
		"struct mmsghdr *":            "u64",
		"struct mq_attr *":            "u64",
		"struct msgbuf *":             "u64",
		"struct msqid_ds *":           "u64",
		"struct new_utsname *":        "u64",
		"struct perf_event_attr *":    "u64",
		"struct pollfd *":             "u64",
		"struct rlimit64 *":           "u64",
		"struct rlimit *":             "u64",
		"struct robust_list_head * *": "u64",
		"struct robust_list_head *":   "u64",
		"struct rusage *":             "u64",
		"struct sched_attr *":         "u64",
		"struct sched_param *":        "u64",
		"struct sembuf *":             "u64",
		"struct shmid_ds *":           "u64",
		"struct sigaction *":          "u64",
		"struct sigevent *":           "u64",
		"struct siginfo *":            "u64",
		"struct sockaddr *":           "u64",
		"struct stat *":               "u64",
		"struct statfs *":             "u64",
		"struct __sysctl_args *":      "u64",
		"struct sysinfo *":            "u64",
		"struct timespec *":           "u64",
		"struct timeval *":            "u64",
		"struct timex *":              "u64",
		"struct timezone *":           "u64",
		"struct tms *":                "u64",
		"struct user_msghdr *":        "u64",
		"struct ustat *":              "u64",
		"struct utimbuf *":            "u64",
		"timer_t *":                   "u64",
		"timer_t":                     "timer_t",
		"time_t *":                    "u64",
		"u32 *":                       "s64",
		"u32":                         "u32",
		"u64":                         "u64",
		"__u64":                       "u64",
		"uid_t *":                     "u64",
		"uid_t":                       "uid_t",
		"umode_t":                     "u64",
		"union bpf_attr *":            "u64",
		"unsigned char *":             "char",
		"unsigned *":                  "u64",
		"unsigned":                    "unsigned",
		"unsigned int *":              "u64",
		"unsigned int":                "u64",
		"unsigned long *":             "u64",
		"unsigned long":               "unsigned long",
		"void *":                      "u64",
	}
)

const networkTemplate = `
// network events structs

type ConnectV4Event struct {
	Saddr 		uint32
	Daddr		uint32
	Sport		uint16
	Dport		uint16
	Netns		uint32
}

type ConnectV6Event struct {
	Saddr 		[16]byte
	Daddr		[16]byte
	Sport		uint16
	Dport		uint16
	Netns		uint32
}

// network events string functions

func (e ConnectV4Event) String(ret int64) string {
	return fmt.Sprintf("Saddr %s Daddr %s Sport %d Dport %d Netns %d ", inet_ntoa(e.Saddr),
		inet_ntoa(e.Daddr), e.Sport, e.Dport, e.Netns)
}

func (e ConnectV6Event) String(ret int64) string {
	return fmt.Sprintf("Saddr %s Daddr %s Sport %d Dport %d Netns %d ", inet_ntoa6(e.Saddr),
		inet_ntoa6(e.Daddr), e.Sport, e.Dport, e.Netns)
}

func (e ConnectV4Event) GetArgN(n int, ret int64) (string, error) {
	return "", fmt.Errorf("ConnectV4Event.GetArgN not implemented")
}

func (e ConnectV6Event) GetArgN(n int, ret int64) (string, error) {
	return "", fmt.Errorf("ConnectV6Event.GetArgN not implemented")
}


func (e ConnectV4Event) Metric() *Metric {
	return &Metric{
		ConnectV4Event: &ProtobufConnectV4Event{
			Saddr: e.Saddr,
			Daddr: e.Daddr,
			Sport: uint32(e.Sport),
			Dport: uint32(e.Dport),
			Netns: e.Netns,
		},
	}
}

func (e ConnectV6Event) Metric() *Metric {
	return &Metric{
		ConnectV6Event: &ProtobufConnectV6Event{
			Saddr: inet_ntoa6(e.Saddr),
			Daddr: inet_ntoa6(e.Daddr),
			Sport: uint32(e.Sport),
			Dport: uint32(e.Dport),
			Netns: e.Netns,
		},
	}
}

// network helper functions

func inet_ntoa(ip uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", byte(ip), byte(ip >> 8), byte(ip >> 16), byte(ip >> 24))
}

func inet_ntoa6(ip [16]byte) string {
	return fmt.Sprintf("%v", net.IP(ip[:]))
}
`

const fileTemplate = `
// file events struct

type FileEvent struct {
	Fd    uint64
	Ino   uint64
	Major uint64
	Minor uint64
}
`

const goStructTemplate = `
type {{ .Name }} struct {
	{{- range $index, $param := .Params }}
	{{ $param.Name }} {{ $param.Type }}
	{{- if (eq $param.NeedsPath true) }}
	{{ $param.Name }}Path string
	{{- end }}
	{{- end }}
}
`

const cStructTemplate = `
typedef struct {
	// fields matching struct CommonEvent from tracer.go
	common_event_t common;

	// fields matching the struct for {{ .Name }} from event-structs-generated.go
	{{- range $index, $param := .Params}}
	{{ $param.Type }} {{ $param.Name }}{{ $param.Suffix }};
	{{- end }}
} {{ .Name }}_event_t;
`

const helpers = `
// helpers for events

func min(x, y int) int {
	if x > y {
		return y
	}
	return x
}

// Assume buffer truncates at 0
func bufLen(buf [256]byte) int {
	for idx := 0; idx < len(buf); idx++ {
		if buf[idx] == 0 {
			return idx
		}
	}
	return len(buf)
}

type Event interface {
	String(ret int64) string
	GetArgN(n int, ret int64) (string, error)
	Metric() *Metric
}

func procLookupPath(pid, fd uint32) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", pid, fd))
}

type DefaultEvent struct{}

func (w DefaultEvent) String(ret int64) string {
	return ""
}

func (e DefaultEvent) GetArgN(n int, ret int64) (string, error) {
	return "", fmt.Errorf("DefaultEvent.GetArgN not implemented")
}

func (w DefaultEvent) Metric() *Metric {
	return nil
}
`

// TODO: Use template functions for parameter types & names, make buffer len decision generic for other syscalls
const eventStringsTemplate = `
func (e {{ .Name }}) String(ret int64) string {
	{{- $name := .Name }}
	{{- range $index, $param := .Params }}
		{{- if or (eq $param.Name "Buf") (eq $param.Name "Filename") (eq $param.Name "Pathname") }}
	buffer := (*C.char)(unsafe.Pointer(&e.{{ $param.Name }}))
	length := C.int(0)
			{{- if or (eq $name "ReadEvent") (eq $name "WriteEvent") }}
	if ret > 0 {
		length = C.int(min(int(ret), len(e.{{ $param.Name }})))
	}
			{{- else}}
	length = C.int(bufLen(e.{{ $param.Name }}))
			{{- end}}
	bufferGo := C.GoStringN(buffer, length)
		{{- end }}
	{{- end }}
	return fmt.Sprintf("{{- range $index, $param := .Params -}}
	{{ $param.Name }}
	{{- if or (eq $param.Type "uint64") (eq $param.Type "int64") (eq $param.Type "uint32") }} %d{{else}} %q{{ end -}}
	{{- if or (eq $param.Name "Fd") (eq $param.Name "Dfd") -}}
	<%s> {{/* space */}}
	{{- else }} {{ end -}}
	{{- end }}", {{/* space */}}
	{{- range $index, $param := .Params -}}
		{{- if $index }}, {{ end }}
		{{- if or (eq $param.Name "Buf") (eq $param.Name "Filename") (eq $param.Name "Pathname") -}}
			 bufferGo
			{{- else -}}
			 e.{{ $param.Name }}
		{{- end -}}
		{{- if or (eq $param.Name "Fd") (eq $param.Name "Dfd") -}}
			, e.{{ $param.Name }}Path
		{{- end -}}
	{{- end -}})
}

func (e {{ .Name }}) GetArgN(n int, ret int64) (string, error) {
	switch n {
	{{- range $index, $param := .Params }}
	case {{ $index }}: // {{ $param.Name }}: type {{ $param.Type }}
	{{- if or (eq $param.Type "uint64") (eq $param.Type "int64") (eq $param.Type "uint32") }}
		bufferGo := fmt.Sprintf("%v", e.{{ $param.Name }})
	{{- else }}
		buffer := (*C.char)(unsafe.Pointer(&e.{{ $param.Name }}))
		length := C.int(0)
			{{- if or (eq $name "ReadEvent") (eq $name "WriteEvent") }}
		if ret > 0 {
			length = C.int(min(int(ret), len(e.{{ $param.Name }})))
		}
			{{- else}}
		length = C.int(bufLen(e.{{ $param.Name }}))
			{{- end}}
		bufferGo := C.GoStringN(buffer, length)
	{{- end }}
		return bufferGo, nil
	{{- end }}
	default:
		return "", fmt.Errorf("Event {{ .Name }} does not have argument %d", n)
	}
}
`

const eventProtobufTemplate = `
func (e {{ .Syscall.Name }}) Metric() *Metric {
	return &Metric{
		{{ .Syscall.Name }}: &Protobuf{{ .Syscall.Name }}{
		{{- range $index, $param := .Syscall.Params }}
			{{- if or (eq $param.Name "Buf") (eq $param.Name "Filename") (eq $param.Name "Pathname") }}
				{{ $param.Name }}: e.{{ $param.Name }}[:],
			{{- else }}
				{{ $param.Name }}: e.{{ $param.Name }},
			{{- end }}
		{{- end }}
		},
	}
}
`

const getStructPreamble = `
func GetStruct(ce *CommonEvent, ctx Context, buf *bytes.Buffer) (Event, error) {
	switch ce.Name {
`

const getStructTemplate = `
	case "{{ .RawName }}":
		ev := {{ .Name }}{}
		{{- range $index, $param := .Params }}
			{{- if eq $param.Type "[256]byte" }}
		copy(ev.{{ $param.Name }}[:], buf.Next(256))
			{{- else if or (eq $param.Type "uint32") (eq $param.Type "int32") }}
		ev.{{ $param.Name }} = {{ $param.Type }}(binary.LittleEndian.Uint32(buf.Next(4)))
			{{- else if or (eq $param.Type "uint64") (eq $param.Type "int64") }}
		ev.{{ $param.Name }} = {{ $param.Type }}(binary.LittleEndian.Uint64(buf.Next(8)))
			{{- end }}
			{{- if (eq $param.NeedsPath true) }}
		fileName := "unknown"
		info, ok := ctx.Fds.Get(uint32(ce.Pid), uint32(ev.{{ $param.Name }}))
		if ok {
			var stat syscall.Stat_t
			path := filepath.Join("/proc", strconv.FormatInt(int64(ce.Pid), 10), "root", info.Path)
			err := syscall.Stat(path, &stat)
			if err != nil {
				if err == syscall.ENOENT {
					// the file doesn't exist anymore, it's probably "info.Path"
					// but we're not sure
					fileName = fmt.Sprintf("[deleted] (%q)?", info.Path)
				}
			}
			if info.Ino == stat.Ino &&
				info.Major == stat.Dev>>8 &&
				info.Minor == stat.Dev&0xff {
				fileName = info.Path
			}
		}
		ev.{{ $param.Name }}Path = fileName
		{{- end }}
		{{- end }}

	{{- if (eq .Name "CloseEvent") }}
		ctx.Fds.Delete(uint32(ce.Pid), uint32(ev.Fd))
	{{- end }}

		return ev, nil
`

const getStructEpilogue = `
	// file events
	case "fd_install":
		ev := FileEvent{}
		if err := binary.Read(buf, binary.LittleEndian, &ev); err != nil {
			return nil, err
		}
		name, err := procLookupPath(uint32(ce.Pid), uint32(ev.Fd))
		if err != nil {
			name = "unknown"
		}

		fdInfo := FdInfo{Path: name, Ino: ev.Ino, Major: ev.Major, Minor: ev.Minor}

		// ignore entries not backed by files, like sockets or anonymous inodes
		if strings.HasPrefix(fdInfo.Path, "/") {
			ctx.Fds.Put(uint32(ce.Pid), uint32(ev.Fd), fdInfo)
		}

		return ev, nil
	// network events
	case "close_v4":
		fallthrough
	case "accept_v4":
		fallthrough
	case "connect_v4":
		ev := ConnectV4Event{}
		ev.Saddr = binary.LittleEndian.Uint32(buf.Next(4))
		ev.Daddr = binary.LittleEndian.Uint32(buf.Next(4))
		ev.Sport = binary.LittleEndian.Uint16(buf.Next(2))
		ev.Dport = binary.LittleEndian.Uint16(buf.Next(2))
		ev.Netns = binary.LittleEndian.Uint32(buf.Next(4))
		return ev, nil
	case "close_v6":
		fallthrough
	case "accept_v6":
		fallthrough
	case "connect_v6":
		ev := ConnectV6Event{}
		copy(ev.Saddr[:], buf.Next(16))
		copy(ev.Daddr[:], buf.Next(16))
		ev.Sport = binary.LittleEndian.Uint16(buf.Next(2))
		ev.Dport = binary.LittleEndian.Uint16(buf.Next(2))
		ev.Netns = binary.LittleEndian.Uint32(buf.Next(4))
		return ev, nil
	default:
		return DefaultEvent{}, nil
	}
}
`

const protoHeader = `
syntax = "proto3";
package tracer;

message ProtobufCommonEvent {
	uint64 Timestamp = 1;
	int64 Pid = 2;
	int64 Ret = 3;
	string Name = 4;
	uint64 Hash = 5;
	uint64 Flags = 6;
}

message ProtobufConnectV4Event {
	uint32 Saddr = 1;
	uint32 Daddr = 2;
	uint32 Sport = 3;
	uint32 Dport = 4;
	uint32 Netns = 5;
}

message ProtobufConnectV6Event {
	string Saddr = 1;
	string Daddr = 2;
	uint32 Sport = 3;
	uint32 Dport = 4;
	uint32 Netns = 5;
}
`

var tmplFuncMap = template.FuncMap{
	"incn": func(i, n int) int {
		return i + n
	},
}

const protoStructTemplate = `
message {{ .Name }} {
	{{- range $index, $param := .Params }}
	{{ $param.Type }} {{ $param.Name }} = {{ incn $index 1 }};
	{{- end }}
}
`

const protoMetricCollector = `
service MetricCollector {
	rpc Process (stream Metric) returns (Empty) {}
}

message Empty {}
`

const protoMetricTemplate = `
message Metric {
	uint64 Count = 1;
	ProtobufCommonEvent CommonEvent = 2;
	ProtobufConnectV4Event ConnectV4Event = 3;
	ProtobufConnectV6Event ConnectV6Event = 4;
	{{- range $index, $syscall := . }}
	Protobuf{{ $syscall.Name }} {{ $syscall.Name }} = {{ incn $index 5 }};
	{{- end }}
}
`

type Param struct {
	Position  int
	Name      string
	Type      string
	Suffix    string
	HashFunc  string
	NeedsPath bool `json:"needsPath"`
}

type Syscall struct {
	Name    string
	RawName string
	Params  []Param
}

var consideredSyscalls = map[string]struct{}{
	"open":     {},
	"close":    {},
	"read":     {},
	"write":    {},
	"mkdir":    {},
	"mkdirat":  {},
	"chmod":    {},
	"fchmod":   {},
	"fchmodat": {},
	"chown":    {},
	"fchown":   {},
	"fchownat": {},
}

// Converts a string to CamelCase
func ToCamel(s string) string {
	s = strings.Trim(s, " ")
	n := ""
	capNext := true
	for _, v := range s {
		if v >= 'A' && v <= 'Z' || v >= '0' && v <= '9' {
			n += string(v)
		}
		if v >= 'a' && v <= 'z' {
			if capNext {
				n += strings.ToUpper(string(v))
			} else {
				n += string(v)
			}
		}
		if v == '_' || v == ' ' {
			capNext = true
		} else {
			capNext = false
		}
	}
	return n
}

var re = regexp.MustCompile(`\s+field:(?P<type>.*?) (?P<name>[a-z_0-9]+);.*`)

func parseLine(l string, idx int) (*Param, *Param, *Param, error) {
	n1 := re.SubexpNames()

	r := re.FindAllStringSubmatch(l, -1)
	if len(r) == 0 {
		return nil, nil, nil, nil
	}
	res := r[0]

	mp := map[string]string{}
	for i, n := range res {
		mp[n1[i]] = n
	}

	if _, ok := mp["type"]; !ok {
		return nil, nil, nil, nil
	}
	if _, ok := mp["name"]; !ok {
		return nil, nil, nil, nil
	}

	// ignore
	if mp["name"] == "__syscall_nr" {
		return nil, nil, nil, nil
	}

	var goParam Param
	goParam.Name = ToCamel(mp["name"])
	goParam.Type = goTypeConversions[mp["type"]]
	goParam.Suffix = ""
	goParam.Position = 0
	if goParam.Name == "Fd" || goParam.Name == "Dfd" {
		goParam.NeedsPath = true
	}

	var cParam Param
	cParam.Name = mp["name"]
	cParam.Type = cTypeConversions[mp["type"]]

	var protoParam Param
	protoParam.Name = mp["name"]
	protoParam.Type = protoTypeConversions[goParam.Type]

	// TODO: Separate this function when types to check start increasing
	// Build suffix here for expected char pointer. Consider all chars need suffix
	if cTypeConversions[mp["type"]] == "char" {
		cParam.Suffix = fmt.Sprintf("[%d]", maxBufferSize)
	} else {
		cParam.Suffix = ""
	}
	// The position is calculated based on the event format. The actual parameters
	// start from 8th index, hence we subtract that from idx to get position
	// of the parameter to the syscall
	cParam.Position = idx - 8
	// TODO: Add position info here and use the Param struct to populate parameter reading in kretprobe handler

	return &goParam, &cParam, &protoParam, nil
}

func parseSyscall(name, format string) (*Syscall, *Syscall, *Syscall, error) {
	syscallParts := strings.Split(format, "\n")
	var skipped bool

	var cParams []Param
	var goParams []Param
	var protoParams []Param
	for idx, line := range syscallParts {
		if !skipped {
			if len(line) != 0 {
				continue
			} else {
				skipped = true
			}
		}
		gp, cp, protop, err := parseLine(line, idx)
		if err != nil {
			return nil, nil, nil, err
		}
		if gp != nil {
			goParams = append(goParams, *gp)
		}
		if cp != nil {
			cParams = append(cParams, *cp)
		}
		if protop != nil {
			protoParams = append(protoParams, *protop)
		}
	}

	return &Syscall{
			Name:    fmt.Sprintf("%s%s", ToCamel(name), "Event"),
			RawName: name,
			Params:  goParams,
		},
		&Syscall{
			Name:    fmt.Sprintf("%s", name),
			RawName: name,
			Params:  cParams,
		},
		&Syscall{
			Name:    fmt.Sprintf("%s%s%s", "Protobuf", ToCamel(name), "Event"),
			RawName: name,
			Params:  protoParams,
		}, nil
}

func GatherSyscalls() ([]Syscall, []Syscall, []Syscall, error) {
	var goSyscalls []Syscall
	var cSyscalls []Syscall
	var protoSyscalls []Syscall

	err := filepath.Walk(syscallsPath, func(path string, f os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if path == "syscalls" {
			return nil
		}

		if !f.IsDir() {
			return nil
		}

		eventName := f.Name()
		if strings.HasPrefix(eventName, "sys_exit") {
			return nil
		}

		syscallName := strings.TrimPrefix(eventName, "sys_enter_")

		if _, ok := consideredSyscalls[syscallName]; !ok {
			return nil
		}

		formatFilePath := filepath.Join(syscallsPath, eventName, "format")
		formatFile, err := os.Open(formatFilePath)
		if err != nil {
			return nil
		}
		defer formatFile.Close()

		formatBytes, err := ioutil.ReadAll(formatFile)
		if err != nil {
			return err
		}

		goSyscall, cSyscall, protoSyscall, err := parseSyscall(syscallName, string(formatBytes))
		if err != nil {
			return err
		}

		goSyscalls = append(goSyscalls, *goSyscall)
		cSyscalls = append(cSyscalls, *cSyscall)
		protoSyscalls = append(protoSyscalls, *protoSyscall)

		return nil
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("error walking %q: %v", err)
	}
	return goSyscalls, cSyscalls, protoSyscalls, nil
}

func GenerateGoStructs(goSyscalls []Syscall) (string, error) {
	buf := new(bytes.Buffer)

	if _, err := buf.WriteString(kernelStructs); err != nil {
		return "", fmt.Errorf("error writing to buffer: %v", err)
	}

	for _, sc := range goSyscalls {
		goTmpl, err := template.New("go").Parse(goStructTemplate)
		if err != nil {
			return "", fmt.Errorf("error templating: %v", err)
		}
		goTmpl.Execute(buf, sc)
	}

	if _, err := buf.WriteString(helpers); err != nil {
		return "", fmt.Errorf("error writing to buffer: %v", err)
	}

	for _, sc := range goSyscalls {
		goTmpl, err := template.New("go_ev").Parse(eventStringsTemplate)
		if err != nil {
			return "", fmt.Errorf("error templating event String functions: %v", err)
		}
		goTmpl.Execute(buf, sc)
	}

	if _, err := buf.WriteString(getStructPreamble); err != nil {
		return "", fmt.Errorf("error writing to buffer: %v", err)
	}

	for _, sc := range goSyscalls {
		goTmpl, err := template.New("go_getStruct").Parse(getStructTemplate)
		if err != nil {
			return "", fmt.Errorf("error templating getStruct function: %v", err)
		}
		goTmpl.Execute(buf, sc)
	}

	if _, err := buf.WriteString(getStructEpilogue); err != nil {
		return "", fmt.Errorf("error writing to buffer: %v", err)
	}

	if _, err := buf.WriteString(networkTemplate); err != nil {
		return "", fmt.Errorf("error writing to buffer: %v", err)
	}

	for _, sc := range goSyscalls {
		goTmpl, err := template.New("go_protoMessage").Parse(eventProtobufTemplate)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error templating Go protoMessage function: %v\n", err)
			os.Exit(1)
		}
		tmplData := struct {
			Syscall       Syscall
			ProtoTypeConv map[string]string
		}{
			sc,
			protoTypeConversions,
		}
		goTmpl.Execute(buf, tmplData)
	}

	if _, err := buf.WriteString(fileTemplate); err != nil {
		return "", fmt.Errorf("error writing to buffer: %v", err)
	}

	return buf.String(), nil
}

func GenerateCStructs(cSyscalls []Syscall) (string, error) {
	buf := new(bytes.Buffer)

	if _, err := buf.WriteString(headers + headersC); err != nil {
		return "", fmt.Errorf("error writing to buffer: %v", err)
	}

	for _, sc := range cSyscalls {
		cTmpl, err := template.New("C").Parse(cStructTemplate)
		if err != nil {
			return "", fmt.Errorf("error templating: %v", err)
		}
		cTmpl.Execute(buf, sc)
	}

	return buf.String(), nil
}

func GenerateProtoStructs(protoSyscalls []Syscall, goSyscalls []Syscall) (string, error) {
	buf := new(bytes.Buffer)

	if _, err := buf.WriteString(headers); err != nil {
		return "", fmt.Errorf("error writing to buffer: %v", err)
	}

	buf.Write([]byte(protoHeader))

	for _, sc := range protoSyscalls {
		tmpl, err := template.New("proto").Funcs(tmplFuncMap).Parse(protoStructTemplate)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error templating proto: %v\n", err)
			os.Exit(1)
		}
		tmpl.Execute(buf, sc)
	}

	buf.Write([]byte(protoMetricCollector))

	tmpl, err := template.New("proto").Funcs(tmplFuncMap).Parse(protoMetricTemplate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error templating proto: %v\n", err)
		os.Exit(1)
	}
	tmpl.Execute(buf, goSyscalls)

	return buf.String(), nil
}
