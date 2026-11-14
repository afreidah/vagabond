// -------------------------------------------------------------------------------
// Build Script
//
// Author: Alex Freidah
//
// Generates the Rust for the agent, executor and guest protocols before the
// crate compiles. The protos are read where the Go side reads them, so paths
// start at the repository root, two directories up.
// -------------------------------------------------------------------------------

use ttrpc_codegen::{Codegen, Customize, ProtobufCustomize};

// PROTOS are the protocol files, relative to this crate's directory.
const PROTOS: [&str; 3] = [
    "../../internal/agentrpc/agent.proto",
    "../../internal/agentrpc/executor.proto",
    "../../rust/vagabond-firecracker/proto/guest.proto",
];

// main writes the generated code to OUT_DIR, Cargo's directory for build
// output, and has Cargo rerun it whenever a proto changes.
fn main() {
    for proto in PROTOS {
        println!("cargo::rerun-if-changed={proto}");
    }

    Codegen::new()
        .inputs(PROTOS)
        .include("../..")
        .rust_protobuf()
        .customize(Customize {
            async_all: true,
            gen_mod: true,
            ..Default::default()
        })
        .rust_protobuf_customize(ProtobufCustomize::default().gen_mod_rs(true))
        .run()
        .expect("generating the protocol code");

    // include! cannot take inner attributes, and the service generator starts
    // mod.rs with one, so drop those lines before lib.rs includes it.
    let out_dir = std::env::var("OUT_DIR").expect("Cargo sets OUT_DIR");
    let mod_rs = format!("{out_dir}/mod.rs");
    let text = std::fs::read_to_string(&mod_rs).expect("reading mod.rs");
    let kept: Vec<&str> = text
        .lines()
        .filter(|line| !line.starts_with("#!"))
        .collect();
    std::fs::write(&mod_rs, kept.join("\n")).expect("writing mod.rs");
}
