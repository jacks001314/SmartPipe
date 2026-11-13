"""Run from the Linux verification directory containing source/backend and worker.mjs."""
import json, os, pathlib, subprocess, tempfile, uuid, shutil, time
root=pathlib.Path(__file__).resolve().parent
out=root/'pg16-results';out.mkdir(exist_ok=True)
name='smartpipe-pg16-test-'+uuid.uuid4().hex[:12]
private=pathlib.Path(tempfile.mkdtemp(prefix='smartpipe-pg16-home-'))
env=os.environ.copy()
for k in list(env):
 if k.startswith('PG') or k=='SMARTPIPE_TEST_DSN':env.pop(k)
for k in ['GOPATH','GOCACHE']:env[k]=subprocess.check_output(['go','env',k],text=True).strip()
for k in ['HOME','CODEX_HOME','SMARTPIPE_CODEX_CONFIG','GIT_CONFIG_GLOBAL','GIT_CONFIG_SYSTEM']:env[k]=str(private/k)
(private/'HOME').mkdir();env['GIT_CONFIG_NOSYSTEM']='1';env['PGPASSFILE']=str(private/'absent');env['SMARTPIPE_WORKER_JS']=str(root/'worker.mjs')
docker=['sudo','-n','docker'];created=False;counts={'pass':0,'fail':0,'skip':0};failures=[]
def call(args):return subprocess.check_output(args,text=True).strip()
try:
 call(docker+['run','-d','--name',name,'--label','smartpipe.verification=pg16','-e','POSTGRES_HOST_AUTH_METHOD=trust','-e','POSTGRES_DB=smartpipe_test','-p','127.0.0.1::5432','postgres:16']);created=True
 for attempt in range(60):
  if subprocess.run(docker+['exec',name,'pg_isready','-U','postgres'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0:break
  time.sleep(1)
 else:raise RuntimeError('PG16 did not become ready')
 version=call(docker+['exec',name,'psql','-U','postgres','-d','smartpipe_test','-Atc','SHOW server_version'])
 if not version.startswith('16.'):raise RuntimeError('Unexpected version '+version)
 (out/'version.txt').write_text(version+'\n')
 port=call(docker+['port',name,'5432/tcp']).rsplit(':',1)[1]
 env['SMARTPIPE_TEST_DSN']=f'host=127.0.0.1 port={port} user=postgres dbname=smartpipe_test sslmode=disable'
 for pkg in ['testdb','store','testlab','adminapi','releasemcp','patch']:
  path=out/(pkg+'.jsonl')
  with path.open('w') as stream:r=subprocess.run(['go','test','-json','-count=1','-timeout=180s','./internal/'+pkg],cwd=root/'source/backend',env=env,stdout=stream,stderr=subprocess.STDOUT)
  if r.returncode:failures.append(pkg+': exit '+str(r.returncode))
  for line in path.read_text().splitlines():
   try:event=json.loads(line)
   except ValueError:continue
   if event.get('Test') and event.get('Action') in counts:
    counts[event['Action']]+=1
    if event['Action']=='skip':failures.append(pkg+': skipped '+event['Test'])
finally:
 if created:
  call(docker+['rm','-f','-v',name])
  (out/'cleanup.txt').write_text('Removed isolated container and anonymous volumes: '+name+'\n')
 shutil.rmtree(private)
summary={'counts':counts,'failures':failures,'status':'FAIL' if failures else 'PASS'}
(out/'summary.json').write_text(json.dumps(summary,indent=2)+'\n');print(json.dumps(summary))
raise SystemExit(1 if failures else 0)
