import { spawnSync } from 'node:child_process';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
const root=dirname(fileURLToPath(import.meta.url));
for(const mod of ['cua-sdk','node-repl']) {
 const cwd=join(root,mod);
 // Invoke JS through node directly; no Windows shell or npx dependency.
 const command=mod==='cua-sdk'
  ? [join(cwd,'node_modules/typescript/bin/tsc'),'-p','tsconfig.json']
  : [join(cwd,'build.mjs')];
 const r=spawnSync(process.execPath,command,{cwd,stdio:'inherit',shell:false});
 if(r.error) throw r.error;
 if(r.status!==0)process.exit(r.status??1);
}
console.log('SmartPipe desktop modules built. Native driver readiness is checked separately.');
