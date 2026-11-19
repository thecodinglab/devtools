{
  lib,
  stdenv,
  fetchzip,
  pkg-config,
  zig_0_17,
  cctools,
  fixDarwinDylibNames,
  xcbuild,

  optimize ? "ReleaseFast",
  simd ? true,
}:
stdenv.mkDerivation (finalAttrs: {
  pname = "libghostty-vt";
  version = "1.0.1";

  # Release tarball, not the GitHub archive of tag vt-1.0.1: only the release
  # tarball has LIB_VERSION, which makes `zig build` build the library
  # (PACKAGING-LIBGHOSTTY.md, "Source Tarballs").
  src = fetchzip {
    url = "https://release.files.ghostty.org/libghostty-vt/${finalAttrs.version}/libghostty-vt-${finalAttrs.version}.tar.gz";
    hash = "sha256-zGuLawiLRplgXoWllDefbR+QYoEiS0MhHG+EKWFFMjI=";
  };

  # Companion tarball: every Zig package, unpacked, named by Zig package hash.
  # It is the directory `zig build --system` wants; real directories, no
  # symlink farm (see nixpkgs' note on codeberg.org/ziglang/zig/issues/32121).
  deps = fetchzip {
    url = "https://release.files.ghostty.org/libghostty-vt/${finalAttrs.version}/libghostty-vt-${finalAttrs.version}-deps.tar.gz";
    hash = "sha256-bqi6TjqxfMtLYyStfHeqZBPsapfdJHVT3ILxL7u+kEQ=";
  };

  strictDeps = true;

  nativeBuildInputs = [
    pkg-config
    zig_0_17
  ]
  ++ lib.optionals stdenv.hostPlatform.isDarwin [
    cctools
    fixDarwinDylibNames
    xcbuild
  ];

  # Darwin only; the Linux derivation is not affected. UNVERIFIED: never run
  # on a Mac. The first three rewrites are the ones nixpkgs applies to its
  # 2026-08-06 snapshot. The last one is new: since vt-1.0.1 the script that
  # post-processes the static archive calls /usr/bin/xcrun and friends by
  # absolute path (src/build/libsystem_override.sh), which a sandboxed Nix
  # build cannot reach.
  postPatch = lib.optionalString stdenv.hostPlatform.isDarwin ''
    substituteInPlace src/build/LibtoolStep.zig \
      --replace-fail /bin/cp cp \
      --replace-fail /usr/bin/ranlib ranlib
    substituteInPlace src/build/GhosttyLibVt.zig \
      --replace-fail /bin/ln ln
    substituteInPlace pkg/apple-sdk/native_link.zig \
      --replace-fail /usr/bin/xcrun xcrun
    sed -i -E \
      -e 's#/usr/bin/(xcrun|awk|mktemp|sed|sort)#\1#g' \
      -e 's#/bin/(chmod|cp|mkdir|rm)#\1#g' \
      src/build/libsystem_override.sh
  '';

  dontSetZigDefaultFlags = true;

  zigBuildFlags = [
    "--system"
    "${finalAttrs.deps}"
    "-Dcpu=baseline"
    "-Doptimize=${optimize}"
    "-Dsimd=${lib.boolToString simd}"
  ];

  doCheck = false;

  outputs = [
    "out"
    "dev"
  ];

  postInstall = ''
    mkdir -p "$dev/lib"
    mv "$out/lib/libghostty-vt.a" "$dev/lib/"
  '';

  postFixup = ''
    substituteInPlace "$dev/lib/pkgconfig/libghostty-vt-static.pc" \
      --replace-fail "$out" "$dev"
  '';

  meta = {
    description = "Ghostty's terminal emulation library (C ABI), release ${finalAttrs.version}";
    homepage = "https://lib.ghostty.org/";
    license = lib.licenses.mit;
    pkgConfigModules = [
      "libghostty-vt"
      "libghostty-vt-static"
    ];
    platforms = [
      "x86_64-linux"
      "aarch64-linux"
      "aarch64-darwin"
    ];
  };
})
