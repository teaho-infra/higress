#!/usr/bin/env python3
"""Real Redis Lua integration; use disposable Redis only. Unique prefix per run."""
import socket,json,uuid,os,pathlib
s=socket.create_connection(('127.0.0.1',int(os.getenv('QUOTA_TEST_REDIS_PORT','16389'))));f=s.makefile('rb')
def read():
 t=f.read(1);v=f.readline().strip()
 if t==b'$':
  n=int(v)
  if n<0:return None
  x=f.read(n);f.read(2);return x.decode()
 if t==b'-':raise RuntimeError(v)
 if t==b'*':return [read() for _ in range(int(v))]
 return v.decode()
def cmd(*args):
 a=[str(x).encode() for x in args];s.sendall(b'*'+str(len(a)).encode()+b'\r\n'+b''.join(b'$'+str(len(x)).encode()+b'\r\n'+x+b'\r\n' for x in a));return read()
prefix='isolated-test:'+str(uuid.uuid4())+':'
script=pathlib.Path('ledger.lua').read_text();tiers={'L1':{'quota':330000000,'models':[]},'L2':{'quota':1000000000,'models':[]},'L3':{'quota':2300000000,'models':[]}}
def run(action,**kw):
 r=dict(action=action,period='2026-11-09',dimension='app',subject='app1',ticket_id=str(uuid.uuid4()),operator='test',occurred_at='test');r.update(kw);r['fingerprint']=json.dumps({k:v for k,v in r.items() if k!='occurred_at'},sort_keys=True);return json.loads(cmd('EVAL',script,1,prefix,json.dumps(r),json.dumps(tiers)))
checks=[]
def check(name,condition):
 assert condition,name
 checks.append(name)
run('assign',tier='L3')
a=run('query');check('64 bit quota',a['limit']==2300000000)
run('assign',dimension='user',subject='u',app='app1',tier='L2')
a=run('check',app='app1',subject='u');check('lower user inherits app',a['payer_dimension']=='app')
run('tier',dimension='user',subject='u',tier='L3')
a=run('check',app='app1',subject='u');check('equal tier user wins',a['payer_dimension']=='user')
run('settle',app='app1',subject='u',payer_dimension='user',value=3000000000,ticket_id='response1')
run('settle',app='app1',subject='u',payer_dimension='user',value=3000000000,ticket_id='response1')
u=run('query',dimension='user',subject='u');a=run('query');check('settle once dual usage',u['usage']==3000000000 and a['usage']==3000000000 and a['spent']==0)
check('noquota stable code',run('check',app='app1',subject='u')['error']=='ai-quota-walmart.noquota')
run('refresh',dimension='user',subject='u');check('refresh preserves spending',run('query',dimension='user',subject='u')['spent']==3000000000)
run('delta',dimension='user',subject='u',value=1000000000,ticket_id='ticket1');run('delta',dimension='user',subject='u',value=1000000000,ticket_id='ticket1')
check('idempotent delta',run('query',dimension='user',subject='u')['limit']==3300000000)
check('conflicting ticket',run('delta',dimension='user',subject='u',value=1,ticket_id='ticket1')['status']==409)
check('downgrade rejected',run('tier',dimension='user',subject='u',tier='L1')['status']==409)
u=run('query',dimension='user',subject='u',period='2026-11-23');check('new period resets temporary tier addon usage',u['tier']=='L2' and u['limit']==1000000000 and u['usage']==0)
run('settle',app='app1',subject='u',payer_dimension='user',value=10,ticket_id='late');check('late response original period',run('query',dimension='user',subject='u')['spent']==3000000010 and run('query',dimension='user',subject='u',period='2026-11-23')['spent']==0)
tiers2=json.loads(json.dumps(tiers));tiers2['L3']['quota']=2400000000;run('tiers',tiers=tiers2);check('templates future only',run('query')['limit']==2300000000 and run('query',period='2026-11-23')['limit']==2400000000)
run('assign',dimension='user',subject='u2',app='app1',tier='L1');u=run('query',dimension='user',subject='u2');check('association snapshot',u['app']=='app1' and u['effective_subject']=='app1')
check('bounded accounts',len(run('accounts',offset=0,limit=1)['accounts'])==1)
run('assign',subject='upgrade-app',tier='L1');run('settle',app='upgrade-app',subject='',payer_dimension='app',value=100,ticket_id='upgrade-spend');run('tier',subject='upgrade-app',tier='L2');check('upgrade preserves spent',run('query',subject='upgrade-app')['quota']==999999900)
models=json.loads(json.dumps(tiers2));models['L1']['models']=['allowed'];models['L3']['models']=['*'];run('tiers',tiers=models);check('model denied',run('check',app='model-app',subject='',model='blocked')['status']==403);check('model allowed',run('check',app='model-app',subject='',model='allowed')['ok']);run('assign',subject='wildcard',tier='L3');check('model wildcard',run('check',app='wildcard',subject='',model='anything')['ok'])
before=run('query',dimension='user',subject='u2');bad=run('delta',dimension='user',subject='u2',app={},value=10,ticket_id='bad-app');check('malformed app rejected before mutation',bad['status']==400 and run('query',dimension='user',subject='u2')['limit']==before['limit'])
run('assign',subject='cap-app',tier='L1');run('delta',subject='cap-app',value=9999999999999-330000000);check('upgrade cap validation',run('tier',subject='cap-app',tier='L3')['status']==400 and run('query',subject='cap-app')['tier']=='L1')
run('assign',subject='negative-app',tier='L3');run('delta',subject='negative-app',value=-2400000000);check('assign negative limit rejected',run('assign',subject='negative-app',tier='L1')['status']==400 and run('query',subject='negative-app')['base_tier']=='L3')
check('own payer effective fields',run('query',subject='upgrade-app')['effective_limit']==1000000000 and run('query',subject='upgrade-app')['effective_balance']==999999900)
print(json.dumps({'ok':True,'checks':checks,'prefix':prefix},indent=2))
# Delete only unique test keys, never shared application data.
keys=cmd('KEYS',prefix+'*')
if keys:cmd('DEL',*keys)
