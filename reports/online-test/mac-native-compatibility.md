# macOS native compatibility result

Official 0.27.0 macOS universal archive completed: 47,104,043 bytes.

The exact verified archive SHA256 is recorded below (authoritative):
48cf02b0c022b8539600686521f7d5988a4372c0515b97c2b5ec6c4214c6363a

Transferred Linux -> Windows -> Mac. Offline provisioning succeeded (exit 0) into /Users/malama/smartpipe-test-assets/native-cache/0.27.0/darwin-universal. check-native.mjs then failed (exit 1), as recorded in mac-native-load.json: Foundation symbol _$s10Foundation22_convertErrorToNSErrorySo0E0Cs0C0_pF unavailable.

Read-only diagnostics on 192.168.1.8:
- sw_vers: macOS 12.4, build 21F79.
- otool -l libcua_driver_sdk.dylib: LC_BUILD_VERSION, platform 1, minos 13.0, sdk 26.5.

The installed OS is below the archive's declared minimum. This is a binary compatibility limitation, not a missing accessibility permission. macOS 13 is the declared minimum, not a tested guarantee of successful loading. No OS upgrade, permission changes, UI session or product acceptance was performed.

Mac native acceptance requires a compatible host or a separately built and validated compatible native runtime. Windows native loading is already verified. Full Syntropy product scenarios remain outstanding.
