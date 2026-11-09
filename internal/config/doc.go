// Package config describes the providers a Vagabond deployment can dispatch to.
//
// Written in HCL, the same syntax job files use, so that an operator reading one
// does not have to learn a second language to read the other.
//
// Configuration says which providers exist, what kind each is, and whatever an
// operator wants to tag them with. It does not carry credentials: a provider's
// credentials block names where its secret comes from, never the secret itself.
package config
