# Spec decoding fixtures

`zkvote.wasm` is a real contract fetched from Stellar **testnet**, checked in so
that `go test ./...` can decode a genuine on-chain interface without a network.

| | |
|---|---|
| Contract | `CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN` |
| WASM hash | `c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781` |
| Size | 10,132 bytes |
| Built with | `soroban-sdk` 25.3.0, rustc 1.93.0-nightly |
| Fetched | 2026-08-07 |

A contract's WASM hash is the SHA-256 of the module, so the fixture can be
verified against its name on chain:

```console
$ sha256sum zkvote.wasm
c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781  zkvote.wasm
```

It was picked because it is small yet covers most of the decoder: functions
with and without doc comments, `Result<T, Error>` returns, `BytesN<N>` of
several widths, `Vec<T>`, references to user-defined types, structs, an error
enum, and an event with both topic and data parameters.

`zkvote.golden.json` is the ABI the decoder is expected to produce from it.
Regenerate it after an intentional change to the output shape:

```console
$ go test ./internal/spec -run TestDecodeFixture -update
```
