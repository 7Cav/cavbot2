// Package utils stands in for cavbot2's utils, which holds HandleError.
package utils

type InteractionResponder interface{}

type InteractionCreate struct{}

// HandleError sends message to the member.
func HandleError(r InteractionResponder, i *InteractionCreate, message string) {}
