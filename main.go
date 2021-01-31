// Copyright 2021 Daniel Erat <dan@erat.org>.
// All rights reserved.

package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "%s <prog.bin>\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if len(flag.Args()) != 1 {
		flag.Usage()
		os.Exit(2)
	}

	f, err := os.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed opening program: ", err)
		os.Exit(1)
	}
	defer f.Close()

	vm, err := newVM(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed reading program %q: %v\n", flag.Arg(0), err)
		os.Exit(1)
	}

	go func(stdin io.Reader) {
		r := bufio.NewReader(stdin)
		for {
			ln, err := r.ReadString('\n')
			if err == io.EOF {
				break
			} else if err != nil {
				fmt.Fprintf(os.Stderr, "Input failed: %v\n", err)
				os.Exit(1)
			}
			for _, ch := range ln {
				vm.in <- byte(ch)
			}
			vm.in <- '\n'
		}
		vm.halt()
	}(os.Stdin)

	done := make(chan struct{}) // closed when program halts
	go func() {
		for v := range vm.out {
			fmt.Print(string(rune(v)))
		}
		close(done)
	}()

	vm.start()
	<-done
	if err := vm.wait(); err != nil {
		fmt.Fprintln(os.Stderr, "Execution failed: ", err)
	}
}
