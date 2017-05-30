package tracer

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"

	elflib "github.com/iovisor/gobpf/elf"

	"github.com/ShiftLeftSecurity/traceleft/probe"
)

// this has to match the struct in trace_syscalls.c and handlers.
type CommonEvent struct {
	Timestamp uint64
	Pid       int64
	Ret       int64
	Syscall   [64]byte
}

type Tracer struct {
	m        *elflib.Module
	perfMap  *elflib.PerfMap
	stopChan chan struct{}
}

func timestamp(data *[]byte) uint64 {
	var event CommonEvent
	err := binary.Read(bytes.NewBuffer(*data), binary.LittleEndian, &event)
	if err != nil {
		fmt.Fprintf(os.Stderr, "timestamp() failed to decode received data: %v\n", err)
		return 0
	}

	return uint64(event.Timestamp)
}

func New(callback func(*[]byte)) (*Tracer, error) {
	globalBPF, err := probe.Load()
	if err != nil {
		return nil, fmt.Errorf("error loading probe: %v", err)
	}

	channel := make(chan []byte)
	perfMap, err := elflib.InitPerfMap(globalBPF, "events", channel, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to init perf map: %v", err)
	}

	perfMap.SetTimestampFunc(timestamp)

	stopChan := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopChan:
				return
			case data := <-channel:
				callback(&data)
			}
		}
	}()

	perfMap.PollStart()

	return &Tracer{
		m:        globalBPF,
		perfMap:  perfMap,
		stopChan: stopChan,
	}, nil
}

func (t *Tracer) Stop() {
	close(t.stopChan)
	t.perfMap.PollStop()

	t.m.Close()
}

func (t *Tracer) BPFModule() *elflib.Module {
	return t.m
}
