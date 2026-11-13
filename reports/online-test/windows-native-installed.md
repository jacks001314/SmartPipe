# Windows native payload verified

Official archive cua-driver-rs-0.27.0-windows-x86_64-binary.zip completed at 29,097,113 bytes. Linux sha256sum matched the pinned official SHA256 b0dc3f6367111804223941f67e5b9bf2f650ee6116f772a73533cb9e01c27c80.

Transferred by scp to the Windows workspace, then ran:

```
node test-runner/modules/cua-sdk/scripts/provision-native.mjs .work/native-cache .work/native-download/cua-driver-rs-0.27.0-windows-x86_64-binary.zip
node test-runner/modules/cua-sdk/scripts/check-native.mjs .work/native-cache reports/online-test/windows-native-load.json
```

Both commands exited 0. The installer independently hashes the local archive before extraction. Actual generated FFI bindings initialized successfully on Windows x64 / Node v24.15.0. The JSON report contains hashes of both loaded native files.

Cache: .work/native-cache/0.27.0/windows-x86_64. Native cache and archives are local runtime artifacts and are not committed. Configure QWEN_CUA_SDK_CACHE_DIR as the absolute cache root when launching the Runner.

No desktop host was created, no permission prompt was driven, no UI input was sent. Actual Syntropy product acceptance remains outstanding; current assistant-hosting UI must not be controlled through an alternate route.
