'use strict';
// Same-run artifact integrity, not a claim of signed remote attestation.
const fs=require('node:fs'),path=require('node:path');
const {hash,walk,verifyGuestSources,ROOT}=require('./native-package');
function guestFiles(directory){
 const roots=fs.readdirSync(directory).sort();
 if(roots.some(n=>!['guest','notices','evidence','guest-export.json'].includes(n)))throw Error('native_cloud_guest_extra_root');
 const result={};let total=0;const seen=new Set();
 for(const name of ['guest','notices','evidence']){
  const root=path.join(directory,name);if(!fs.lstatSync(root).isDirectory()||fs.lstatSync(root).isSymbolicLink())throw Error('native_cloud_guest_invalid');
  for(const [file,identity]of Object.entries(walk(root))){
   const key=name+'/'+file;
   if(key.split('/').some(p=>!p||p==='.'||p==='..'||/[\x00-\x1f\\:*?"<>|]/.test(p)||/[. ]$/.test(p)||/^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)/i.test(p))||seen.has(key.toLowerCase()))throw Error('native_cloud_guest_invalid');
   total+=identity.size;if(identity.size<=0||identity.size>1024**3||total>2*1024**3)throw Error('native_cloud_guest_size_invalid');
   seen.add(key.toLowerCase());result[key]=identity;
  }
 }
 if(Object.keys(result).length>20000||!['guest/vmlinuz','guest/initramfs.cpio.gz','guest/data.img'].every(n=>result[n]))throw Error('native_cloud_guest_incomplete');
 return Object.fromEntries(Object.entries(result).sort(([a],[b])=>a<b?-1:a>b?1:0));
}
function sealGuestExport(directory,commit,repository=ROOT){
 if(!/^[a-f0-9]{40}$/.test(commit||''))throw Error('native_cloud_commit_invalid');
 verifyGuestSources(directory,repository);
 const file=path.join(directory,'guest-export.json');
 fs.writeFileSync(file,JSON.stringify({schemaVersion:1,commit,files:guestFiles(directory)},null,2)+'\n',{flag:'wx'});
 return hash(file);
}
function verifyGuestExport(directory,commit,receiptSHA256,repository=ROOT){
 if(!/^[a-f0-9]{40}$/.test(commit||'')||!/^[a-f0-9]{64}$/.test(receiptSHA256||''))throw Error('native_cloud_identity_invalid');
 const file=path.join(directory,'guest-export.json');
 if(fs.lstatSync(file).isSymbolicLink()||hash(file)!==receiptSHA256)throw Error('native_cloud_receipt_mismatch');
 const receipt=JSON.parse(fs.readFileSync(file));
 if(receipt.schemaVersion!==1||receipt.commit!==commit||JSON.stringify(receipt.files)!==JSON.stringify(guestFiles(directory)))throw Error('native_cloud_payload_mismatch');
 verifyGuestSources(directory,repository);return receipt;
}
module.exports={guestFiles,sealGuestExport,verifyGuestExport};
