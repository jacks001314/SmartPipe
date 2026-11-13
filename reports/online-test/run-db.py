import os,pathlib,tempfile,subprocess,json,hashlib,shutil
root=pathlib.Path(__file__).resolve().parent; out=root/'retest';out.mkdir(exist_ok=True)
log=(out/'commands.log').open('w'); temp=pathlib.Path(tempfile.mkdtemp(prefix='smartpipe-pg-',dir='/tmp')); data=temp/'data';sock=temp/'sock';sock.mkdir(mode=0o700);pg='/usr/lib/postgresql/18/bin/'
env=os.environ.copy()
for k in list(env):
 if k.startswith('PG') or k in ['SMARTPIPE_TEST_DSN']:env.pop(k)
env['PGPASSFILE']=str(temp/'absent-pgpass');env['GOSUMDB']='sum.golang.org'
# Obtain only build-cache paths before isolating HOME; never inspect config/credentials.
for k in ['GOPATH','GOCACHE']:
 env[k]=subprocess.check_output(['go','env',k],env=env,text=True).strip()
for k in ['CODEX_HOME','SMARTPIPE_CODEX_CONFIG','GIT_CONFIG_GLOBAL','GIT_CONFIG_SYSTEM']:
 env[k]=str(temp/('absent-'+k))
env['HOME']=str(temp/'home');pathlib.Path(env['HOME']).mkdir()
env['GIT_CONFIG_NOSYSTEM']='1'
def run(args,**kw):
 log.write('$ '+' '.join(map(str,args))+'\n');log.flush();r=subprocess.run(list(map(str,args)),env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,**kw);log.write(r.stdout.decode(errors='replace'));log.write('\nexit='+str(r.returncode)+'\n');log.flush();return r
started=False
failures=[]
counts={"pass":0,"fail":0,"skip":0}
try:
 r=run([pg+'initdb','-D',data,'-U','sp_test','--auth-local=trust','--auth-host=reject','--no-locale','--encoding=UTF8']);assert r.returncode==0
 r=run([pg+'pg_ctl','-D',data,'-l',temp/'server.log','-o',f"-c listen_addresses='' -c unix_socket_directories='{sock}'",'-w','start']);assert r.returncode==0;started=True
 assert run([pg+'createdb','-h',sock,'-U','sp_test','smartpipe_test']).returncode==0
 run([pg+'psql','-h',sock,'-U','sp_test','-d','smartpipe_test','-c','SELECT version();'])
 env['SMARTPIPE_TEST_DSN']=f'host={sock} user=sp_test dbname=smartpipe_test sslmode=disable'
 run(['go','version'],cwd=root/'source/backend')
 for phase,extra,pkgs in [('desktop',['-run','TestDesktop'],['store','testlab','adminapi','releasemcp']),('full',[],['testdb','store','testlab','adminapi','releasemcp','patch'])]:
  for pkg in pkgs:
   args=['go','test','-json','-count=1','-timeout=180s',*extra,'./internal/'+pkg];log.write('$ '+' '.join(args)+'\n');log.flush()
   with (out/f'{phase}-{pkg}.jsonl').open('w') as f:r=subprocess.run(args,cwd=root/'source/backend',env=env,stdout=f,stderr=subprocess.STDOUT)
   log.write(f'{phase}-{pkg} exit={r.returncode}\n');log.flush()
   if r.returncode:failures.append(f'{phase}-{pkg}: exit {r.returncode}')
   for line in (out/f'{phase}-{pkg}.jsonl').read_text().splitlines():
    try:event=json.loads(line)
    except json.JSONDecodeError:continue
    if event.get('Test') and event.get('Action') in counts:
     counts[event['Action']]+=1
     if event['Action']=='skip':failures.append(f"{phase}-{pkg}: skipped {event['Test']}")
 migration=run([pg+'psql','-h',sock,'-U','sp_test','-d','smartpipe_test','-c','SELECT version,name,applied_at FROM schema_migrations WHERE version BETWEEN 48 AND 52 ORDER BY version;'])
 if migration.returncode:failures.append("migration query failed")
finally:
 if started:
  stop=run([pg+'pg_ctl','-D',data,'-m','fast','-w','stop'])
  if stop.returncode:raise RuntimeError('PostgreSQL stop failed; preserving temporary cluster at '+str(temp))
 if (temp/'server.log').exists():shutil.copyfile(temp/'server.log',out/'postgres.log')
 shutil.rmtree(temp);log.write('CLEANUP temporary cluster removed: '+str(not temp.exists())+'\n');log.close()

summary={"counts":counts,"failures":failures,"status":"FAIL" if failures else "PASS"}
(out/"summary.json").write_text(json.dumps(summary,indent=2)+"\n")
print(json.dumps(summary))
raise SystemExit(1 if failures else 0)
