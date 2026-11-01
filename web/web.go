// Package web 内嵌考生页与管理页，部署时只需要一个二进制。
package web

import _ "embed"

//go:embed candidate.html
var candidateHTML []byte

//go:embed admin.html
var adminHTML []byte

// Candidate 返回考生页。
func Candidate() []byte { return candidateHTML }

// Admin 返回管理页。
func Admin() []byte { return adminHTML }
