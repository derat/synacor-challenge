// Copyright 2021 Daniel Erat <dan@erat.org>.
// All rights reserved.

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	msize = 1 << 15       // number of vals in memory
	nregs = 8             // number of registers
	vmax  = (1 << 15) - 1 // maximum value
	vmod  = vmax + 1      // mod for values
	vreg  = vmax + 1      // value referring to register 0
)

type vm struct {
	mem     [msize]uint16
	reg     [nregs]uint16
	stack   []uint16
	in, out chan byte
	done    chan error
	quit    chan struct{} // halt on next instruction
}

func newVM(r io.Reader) (*vm, error) {
	vm := &vm{
		in:   make(chan byte, 2048),
		out:  make(chan byte, 2048),
		quit: make(chan struct{}),
	}
	var nr int
	for {
		if err := binary.Read(r, binary.LittleEndian, &vm.mem[nr]); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		nr++
	}
	return vm, nil
}

func (vm *vm) start() {
	assertf(vm.done == nil, "already running")
	vm.done = make(chan error, 1)
	go func() {
		vm.done <- vm.run()
		close(vm.done)
	}()
}

func (vm *vm) wait() error {
	assertf(vm.done != nil, "not started")
	return <-vm.done
}

func (vm *vm) halt() {
	close(vm.quit)
}

func (vm *vm) run() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New(r.(string))
		}
		close(vm.out)
	}()

	var ip uint16 // instruction start index
	var sz uint16 // instruction size (including opcode)

	// Returns the value corresponding to the 1-indexed argument.
	// The argument may be either a literal value or a register.
	get := func(arg uint16) uint16 {
		assertf(arg > 0, "invalid arg %v", arg)
		sz = cond(arg+1 > sz, arg+1, sz)
		addr := ip + arg
		av := vm.mem[addr]

		// - numbers 0..32767 mean a literal value
		// - numbers 32768..32775 instead mean registers 0..7
		// - numbers 32776..65535 are invalid
		if av <= vmax { // "numbers 0..32767 mean a literal value"
			return av
		}
		assertf(av < vreg+nregs, "bad value %v at %v", av, addr)
		return vm.reg[av-vreg]
	}

	// Sets the 1-indexed argument to the supplied value.
	// The argument must reference a register.
	set := func(arg uint16, val uint16) {
		assertf(arg > 0, "invalid arg %v", arg)
		sz = cond(arg+1 > sz, arg+1, sz)
		addr := ip + arg
		av := vm.mem[addr]
		assertf(av >= vreg && av < vreg+nregs, "bad register ref %v at %v", av, addr)
		vm.reg[av-vreg] = val
	}

	push := func(v uint16) { vm.stack = append(vm.stack, v) }
	pop := func() uint16 {
		assertf(len(vm.stack) > 0, "pop with empty stack")
		v := vm.stack[len(vm.stack)-1]
		vm.stack = vm.stack[:len(vm.stack)-1]
		return v
	}

	for {
		// Quit if requested.
		select {
		case <-vm.quit:
			return
		default:
		}

		op := vm.mem[ip]
		sz = 1

		switch op {
		case 0: // halt: stop execution and terminate the program
			close(vm.quit)
		case 1: // set a b: set register <a> to the value of <b>
			set(1, get(2))
		case 2: // push a: push <a> onto the stack
			push(get(1))
		case 3: // pop a: remove the top element from the stack and write it into <a>; empty stack = error
			set(1, pop())
		case 4: // eq a b c: set <a> to 1 if <b> is equal to <c>; set it to 0 otherwise
			b, c := get(2), get(3)
			set(1, cond(b == c, 1, 0))
		case 5: // gt a b c: set <a> to 1 if <b> is greater than <c>; set it to 0 otherwise
			b, c := get(2), get(3)
			set(1, cond(b > c, 1, 0))
		case 6: // jmp a: jump to <a>
			ip = get(1)
			sz = 0 // don't advance ip
		case 7: // jt a b: if <a> is nonzero, jump to <b>
			if addr := get(2); get(1) != 0 {
				ip = addr
				sz = 0 // don't advance ip
			}
		case 8: // jf a b: if <a> is zero, jump to <b>
			if addr := get(2); get(1) == 0 {
				ip = addr
				sz = 0 // don't advance ip
			}
		case 9: // add a b c: assign into <a> the sum of <b> and <c> (modulo 32768)
			set(1, (get(2)+get(3))%vmod)
		case 10: // mult a b c: store into <a> the product of <b> and <c> (modulo 32768)
			set(1, uint16((int(get(2))*int(get(3)))%vmod))
		case 11: // mod a b c: store into <a> the remainder of <b> divided by <c>
			set(1, get(2)%get(3))
		case 12: // and a b c: stores into <a> the bitwise and of <b> and <c>
			set(1, get(2)&get(3))
		case 13: // or a b c: stores into <a> the bitwise or of <b> and <c>
			set(1, get(2)|get(3))
		case 14: // not a b: stores 15-bit bitwise inverse of <b> in <a>
			set(1, (^get(2))&vmax)
		case 15: // rmem a b: read memory at address <b> and write it to <a>
			set(1, vm.mem[get(2)])
		case 16: // wmem a b: write the value from <b> into memory at address <a>
			vm.mem[get(1)] = get(2)
		case 17: // call a: write the address of the next instruction to the stack and jump to <a>
			addr := get(1)
			push(ip + sz)
			ip = addr
			sz = 0 // don't advance ip
		case 18: // ret: remove the top element from the stack and jump to it; empty stack = halt
			ip = pop()
			sz = 0 // don't advance ip
		case 19: // out a: write the character represented by ascii code <a> to the terminal
			vm.out <- byte(get(1))
		case 20: // in a: read a character from the terminal and write its ascii code to <a>
			select {
			case v := <-vm.in:
				set(1, uint16(v))
			case <-vm.quit:
				return // interrupt read if requested to quit
			}
		case 21: // nop: no operation
		default:
			panic(fmt.Sprintf("invalid op %v at %v", op, ip))
		}

		ip += sz
	}
	return
}

// cond returns a if c is true and b otherwise.
func cond(c bool, a, b uint16) uint16 {
	if c {
		return a
	}
	return b
}

// assertf panics with the supplied message if v is false.
func assertf(v bool, s string, args ...interface{}) {
	if !v {
		panic(fmt.Sprintf(s, args...))
	}
}

// panicf panics with the supplied message.
func panicf(s string, args ...interface{}) {
	assertf(false, s, args...)
}
