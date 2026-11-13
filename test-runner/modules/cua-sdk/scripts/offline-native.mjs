// Offline transport only: ensureNativePayload still hashes before extraction.
import {createReadStream} from 'node:fs';
import {stat} from 'node:fs/promises';
import {basename} from 'node:path';
import {Readable} from 'node:stream';
export const pinnedArchives = Object.freeze({
 'cua-driver-rs-0.27.0-windows-x86_64-binary.zip':'b0dc3f6367111804223941f67e5b9bf2f650ee6116f772a73533cb9e01c27c80',
 'cua-driver-rs-0.27.0-darwin-universal-binary.tar.gz':'48cf02b0c022b8539600686521f7d5988a4372c0515b97c2b5ec6c4214c6363a',
});
export async function offlineTransport(archive) {
 const name=basename(archive);
 if(!Object.hasOwn(pinnedArchives,name))throw new Error('Unsupported offline archive name');
 const info=await stat(archive);
 if(!info.isFile()||info.size===0)throw new Error('Offline archive must be a nonempty file');
 const base='https://github.com/QwenLM/qwen-code/releases/download/cua-driver-rs-v0.27.0/';
 return async (url)=>{
  if(url===base+'checksums.txt')return new Response(pinnedArchives[name]+'  '+name+'\n');
  if(url===base+name)return new Response(Readable.toWeb(createReadStream(archive)));
  throw new Error('Offline archive does not match requested platform');
 };
}
