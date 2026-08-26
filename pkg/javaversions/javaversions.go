// Lists the java majors runtime images publish
package javaversions

import "fmt"

// Java majors the runtime image publishes
var Supported = []int{8, 11, 17, 21, 25}

// Majors with a published GraalVM variant
var Graal = []int{21, 25}

// Returns the image tag for a java major
func Tag(major int) string {
	return fmt.Sprintf("java%d", major)
}

// Returns the GraalVM variant tag for a java major
func GraalTag(major int) string {
	return fmt.Sprintf("java%d-graal", major)
}

// Reports whether a tag names a published runtime image
func ValidTag(tag string) bool {
	for _, v := range Supported {
		if tag == Tag(v) {
			return true
		}
	}
	for _, v := range Graal {
		if tag == GraalTag(v) {
			return true
		}
	}
	return false
}
