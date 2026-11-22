#!/usr/bin/env python3
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
prefix='money-test:'+str(uuid.uuid4())+':'
script=pathlib.Path('money_ledger.lua').read_text();tiers={t:{'quota':n,'models':[]} for t,n in [('L1',100),('L2',300),('L3',1000)]};prices={'model':{'input_price_micros':1000000,'output_price_micros':2000000}}
def run(action,**kw):
 r=dict(action=action,period='p1',dimension='user',subject='u',app='a',ticket_id=str(uuid.uuid4()),operator='test');r.update(kw);r['fingerprint']=json.dumps({k:v for k,v in r.items() if k!='ticket_id'},sort_keys=True);return json.loads(cmd('EVAL',script,1,prefix,json.dumps(r),json.dumps(tiers),json.dumps(prices)))
checks=[]
def check(name,v):
 assert v,name
 checks.append(name)
run('assign',dimension='department',subject='d',tier='L3')
run('assign',tier='L1',department='d')
check('personal wins',run('query')['limit']==100)
run('assign',subject='v',tier='inherit',department='d')
check('independent inheritance',run('query',subject='v')['limit']==1000)
quote=run('check',model='model');check('user payer and quote',quote['payer_dimension']=='user' and quote['price']==prices['model'])
run('prices',prices={})
check('missing price denies',run('check',model='model')['status']==403)
run('settle',payer_dimension='user',value=60,input_tokens=40,output_tokens=20,cost_micros=80,ticket_id='charge')
run('settle',payer_dimension='user',value=60,input_tokens=40,output_tokens=20,cost_micros=80,ticket_id='charge')
check('charge once frozen quote',run('query')['spent']==80)
check('other user isolated',run('query',subject='v')['spent']==0)
check('app usage cost no budget',run('query',dimension='app',subject='a')['cost_micros']==80 and run('query',dimension='app',subject='a')['spent']==0)
run('delta',value=20,ticket_id='delta');run('delta',value=20,ticket_id='delta')
check('delta idempotent',run('query')['limit']==120)
check('ticket conflict',run('delta',value=21,ticket_id='delta')['status']==409)
run('assign',tier='inherit',department='d');check('reassign preserves addon spent',run('query')['limit']==1020 and run('query')['spent']==80)
run('assign',dimension='department',subject='d',tier='L2')
check('department changes future only',run('query')['limit']==1020 and run('query',period='p2')['limit']==300)
run('tier',tier='L3');run('assign',tier='L1');check('assignment replaces temporary tier',run('query')['limit']==120)
run('settle',payer_dimension='user',value=1,input_tokens=1,output_tokens=0,cost_micros=50)
check('exhausted no fallback',run('check',model='model')['error']=='ai-quota-walmart.noquota')
check('rollover reset',run('query',period='p2')['spent']==0 and run('query',period='p2')['limit']==100)
check('department only assign',run('delta',dimension='department',subject='d',value=1)['status']==400)
check('app cannot assign',run('assign',dimension='app',tier='L3')['status']==400)
check('inherit needs department',run('assign',subject='new',tier='inherit')['status']==400)
check('no user denies',run('check',subject='',model='model')['status']==401)
check('settle negative rejected',run('settle',payer_dimension='user',value=1,input_tokens=1,output_tokens=0,cost_micros=-1)['status']==400)
run('prices',prices={'model':{'input_price_micros':0,'output_price_micros':0}})
check('exhausted free model denied',run('check',model='model')['status']==403)
check('explicit free model accepted',run('check',subject='v',model='model')['price']['input_price_micros']==0)
run('assign',subject='cap',tier='L1')
run('settle',subject='cap',app='cap-app',payer_dimension='user',value=9999999999999,input_tokens=9999999999999,output_tokens=0,cost_micros=0)
check('cap rejects admission',run('check',subject='cap',app='cap-app',model='model')['status']==503)
before=run('query',subject='cap')
check('aggregate overflow rejects before write',run('settle',subject='cap',app='cap-app',payer_dimension='user',value=1,input_tokens=1,output_tokens=0,cost_micros=1)['status']==503 and run('query',subject='cap')['spent']==before['spent'])
check('query does not initialize',run('query',subject='read-only')['quota_source']=='unconfigured' and cmd('HGET',prefix+'ledger:p1','user:read-only') is None)
run('delta',subject='unconfigured',value=100)
check('unconfigured addon cannot authorize',run('check',subject='unconfigured',model='model')['status']==403)
run('assign',subject='near-cap',tier='L1')
run('settle',subject='near-cap',app='near-cap-app',payer_dimension='user',value=1,input_tokens=1,output_tokens=0,cost_micros=9999999999998)
run('assign',subject='fresh',tier='L1')
check('near cap settlement fails',run('settle',subject='fresh',app='near-cap-app',payer_dimension='user',value=1,input_tokens=1,output_tokens=0,cost_micros=2)['status']==503)
check('overflow blocks subsequent admissions',run('check',subject='fresh',app='near-cap-app',model='model')['status']==503)
run('assign',subject='dedup',tier='L1')
run('settle',subject='dedup',app='dedup-app',payer_dimension='user',value=9999999999999,input_tokens=9999999999999,output_tokens=0,cost_micros=0,ticket_id='cap-replay')
check('overflow replay no latch',run('settle',subject='dedup',app='dedup-app',payer_dimension='user',value=9999999999999,input_tokens=9999999999999,output_tokens=0,cost_micros=0,ticket_id='cap-replay')['ok'] and cmd('HEXISTS',prefix+'blocked:p1','app:dedup-app')=='0')
oldprefix=prefix;prefix='money-test:legacy:'+str(uuid.uuid4())+':'
cmd('HSET',prefix+'base','user:legacy','{"tier":"L1"}')
check('legacy namespace rejected',run('meta')['status']==503)
cmd('DEL',prefix+'base');check('fresh read no marker',run('meta')['ok'] and cmd('GET',prefix+'budget_policy') is None)
prefix=oldprefix
saved_script=script;script=pathlib.Path('ledger.lua').read_text()
check('reverse money namespace reuse rejected',run('query')['status']==503)
script=saved_script
print(json.dumps({'ok':True,'checks':checks},indent=2))
keys=cmd('KEYS',prefix+'*')
if keys:cmd('DEL',*keys)
