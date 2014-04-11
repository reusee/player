package main

import "log"

func p(fmt string, args ...interface{}) {
	log.Printf(fmt, args...)
}
