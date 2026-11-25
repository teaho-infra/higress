local p=KEYS[1]
local r=cjson.decode(ARGV[1])
local function fail(code,msg,status) return cjson.encode({ok=false,error='ai-quota-walmart.'..code,message=msg,status=status}) end
local now=tonumber(redis.call('TIME')[1])
local tk=r.ticket_id and p..'ticket:'..r.ticket_id or nil
if tk and r.action~='settle' and r.action~='block' then
 local prior=redis.call('GET',tk)
 if prior then local old=cjson.decode(prior);if old.fingerprint~=r.fingerprint then return fail('ticket_conflict','Ticket already used with different request',409) end;return old.response end
end
local configured=redis.call('GET',p..'cycle_config')
local cycle=configured and cjson.decode(configured) or nil
local function activeCycle()
 if not cycle then return string.sub(r.period_start,1,10),r.period_start_unix,r.period_end_unix,14,14 end
 local boundary=cycle.end_unix or cycle.anchor+cycle.current_days*86400
 if now<boundary then return cycle.current_period,cycle.anchor,boundary,cycle.current_days,cycle.next_days end
 local days=cycle.next_days or cycle.current_days
 local start=boundary+math.floor((now-boundary)/(days*86400))*days*86400
 return 'c'..cycle.revision..':'..start,start,start+days*86400,days,days
end
local activePeriod,periodStart,periodEnd,periodDays,nextPeriodDays=activeCycle()
if r.action~='settle' and r.action~='block' then
 if r.ticket_id and r.period~=activePeriod then return fail('period_conflict','Current period required',409) end
 r.period=activePeriod;r.period_start_unix=periodStart;r.period_end_unix=periodEnd
 if cycle then r.period_start=periodStart;r.period_end=periodEnd end
 if now<periodStart or now>=periodEnd then return fail('period_conflict','Period changed; retry with current period',409) end
end
if r.app~=nil and type(r.app)~='string' then return fail('invalid_request','app must be a string',400) end
local marker=redis.call('GET',p..'budget_policy')
if marker and marker~='personal_department_money' then return fail('storage_policy_conflict','Redis namespace policy mismatch',503) end
if not marker then
 for _,suffix in ipairs({'base','tiers','prices','registry:user','registry:app','registry:department','ledger:'..r.period}) do
  if redis.call('EXISTS',p..suffix)==1 then return fail('storage_policy_conflict','Existing unmarked namespace; use a fresh money prefix',503) end
 end
end
local function mark() redis.call('SET',p..'budget_policy','personal_department_money') end
if r.action=='block' then mark();redis.call('HSET',p..'blocked:'..r.period,'app:'..r.app,'1','user:'..r.subject,'1');return cjson.encode({ok=true}) end
local tiers=cjson.decode(redis.call('GET',p..'tiers') or ARGV[2])
local function save(a) mark();redis.call('HSET',p..'ledger:'..r.period,a.dimension..':'..a.subject,cjson.encode(a));redis.call('ZADD',p..'registry:'..a.dimension,0,a.subject) end
local prices=cjson.decode(redis.call('GET',p..'prices') or ARGV[3] or '{}')
local function base(d,s) return cjson.decode(redis.call('HGET',p..'base',d..':'..s) or '{}') end
local function resolved(b)
 if tiers[b.tier] then return b.tier,'personal' end
 local dep=base('department',b.department or '')
 if tiers[dep.tier] then return dep.tier,'department' end
 return '', 'unconfigured'
end
local function account(d,s,app)
 local b=base(d,s)
 if d=='department' then return {dimension=d,subject=s,tier=b.tier or ''} end
 local a=cjson.decode(redis.call('HGET',p..'ledger:'..r.period,d..':'..s) or '{}')
 if not a.subject then
  local t,source=resolved(b);if d=='app' then t='';source='unconfigured' end
  local lim=tiers[t] and tiers[t].quota or 0
  a={dimension=d,subject=s,app=b.app or app or '',department=b.department or '',tier=t,quota_source=source,base_tier=b.tier or 'inherit',limit=lim,base_quota=lim,usage=0,input_tokens=0,output_tokens=0,cost_micros=0,spent=0,requests=0,period=r.period,models=tiers[t] and tiers[t].models or {}}
 end
 if app and app~='' then a.app=app end
 a.quota=a.limit-a.spent;a.balance=a.quota
 return a
end
local function rank(t) return tonumber(string.sub(t or '',2)) or 0 end
local function snapshot(a)
 if a.dimension=='user' then a.effective_dimension='user';a.effective_subject=a.subject;a.effective_tier=a.tier;a.effective_limit=a.limit;a.effective_balance=a.balance end
 return a
end
if r.action=='meta' then return cjson.encode({ok=true,period=r.period,period_start=r.period_start,period_end=r.period_end,period_days=periodDays,next_period_days=nextPeriodDays,tiers=tiers,tiers_scope='future_initialization',budget_policy='personal_department_money',currency='CNY',amount_unit='micro_yuan',prices=prices}) end
if r.action=='prices' and not r.prices then return cjson.encode({ok=true,prices=prices,period=r.period}) end
if r.action=='accounts' then
 local list=redis.call('ZRANGE',p..'registry:'..r.dimension,r.offset,r.offset+r.limit-1);local out={}
 for _,s in ipairs(list) do table.insert(out,snapshot(account(r.dimension,s))) end
 return cjson.encode({ok=true,period=r.period,accounts=out,next_offset=r.offset+#list})
end
if r.action=='audit' then
 local list=redis.call('LRANGE',p..'audit',r.offset,r.offset+r.limit-1);local out={};for _,s in ipairs(list) do table.insert(out,cjson.decode(s)) end
 return cjson.encode({ok=true,records=out})
end
if r.action=='check' then
 if not r.subject or r.subject=='' then return fail('invalid_subject','Trusted user required',401) end
 local payer=account('user',r.subject,r.app)
 if payer.quota_source=='unconfigured' then return fail('unconfigured','Personal tier or valid department required',403) end
 if redis.call('HEXISTS',p..'blocked:'..r.period,'app:'..r.app)==1 or redis.call('HEXISTS',p..'blocked:'..r.period,'user:'..r.subject)==1 then return fail('counter_overflow','Settlement counter overflow requires administrator reconciliation',503) end
 if payer.quota<=0 then return fail('noquota','No quota left',403) end
 local app=account('app',r.app);for _,a in ipairs({payer,app}) do for _,k in ipairs({'usage','input_tokens','output_tokens','cost_micros','spent','requests'}) do if a[k]>=9999999999999 then return fail('counter_overflow','Counter limit reached',503) end end end
 local price=prices[r.model];if not price then return fail('missing_price','Model price not configured',403) end
 if #payer.models>0 then local allowed=false;for _,m in ipairs(payer.models) do if m==r.model or m=='*' then allowed=true end end;if not allowed then return fail('model_forbidden','Model not allowed by tier',403) end end
 save(payer)
 return cjson.encode({ok=true,payer_dimension='user',payer_subject=payer.subject,period=r.period,price=price})
end
if r.action=='settle' then
 if redis.call('EXISTS',p..'settled:'..r.period..':'..r.ticket_id)==1 then return cjson.encode({ok=true}) end
 if r.payer_dimension~='user' or not r.subject or r.subject=='' then return fail('invalid_subject','User payer required',400) end
 local app=account('app',r.app);local user=account('user',r.subject,r.app)
 for _,key in ipairs({'value','input_tokens','output_tokens','cost_micros'}) do local n=r[key];if type(n)~='number' or n<0 or n~=math.floor(n) or n>9999999999999 then return fail('invalid_amount','Invalid settlement',400) end end
 for _,a in ipairs({user,app}) do
  if a.requests>=9999999999999 then redis.call('HSET',p..'blocked:'..r.period,'app:'..r.app,'1','user:'..r.subject,'1');return fail('counter_overflow','Request counter limit exceeded',503) end
  for _,pair in ipairs({{'usage','value'},{'input_tokens','input_tokens'},{'output_tokens','output_tokens'},{'cost_micros','cost_micros'}}) do if a[pair[1]]+r[pair[2]]>9999999999999 then redis.call('HSET',p..'blocked:'..r.period,'app:'..r.app,'1','user:'..r.subject,'1');return fail('counter_overflow','Counter limit exceeded',503) end end
 end
 if user.spent+r.cost_micros>9999999999999 then redis.call('HSET',p..'blocked:'..r.period,'user:'..r.subject,'1');return fail('counter_overflow','Spend limit exceeded',503) end
 if redis.call('SET',p..'settled:'..r.period..':'..r.ticket_id,'1','NX','EX',5184000)==false then return cjson.encode({ok=true}) end
 for _,a in ipairs({user,app}) do a.usage=a.usage+r.value;a.input_tokens=a.input_tokens+r.input_tokens;a.output_tokens=a.output_tokens+r.output_tokens;a.cost_micros=a.cost_micros+r.cost_micros;a.requests=a.requests+1 end
 user.spent=user.spent+r.cost_micros;save(user);save(app)
 return cjson.encode({ok=true})
end
if r.action=='query' or r.action=='usage' then local a=account(r.dimension,r.subject);a.ok=true;return cjson.encode(snapshot(a)) end
if r.action=='cycle' or r.action=='reset' then
 if r.action=='cycle' and (type(r.days)~='number' or r.days~=math.floor(r.days) or r.days<1 or r.days>365) then return fail('invalid_cycle','days must be from 1 to 365',400) end
 if r.action=='reset' and (r.start_unix~=nil or r.end_unix~=nil) then
  if type(r.start_unix)~='number' or type(r.end_unix)~='number' or r.start_unix~=math.floor(r.start_unix) or r.end_unix~=math.floor(r.end_unix) or r.start_unix>now or r.end_unix<=now or r.end_unix-r.start_unix<86400 or r.end_unix-r.start_unix>365*86400 or (r.end_unix-r.start_unix)%86400~=0 then
   return fail('invalid_cycle','Reset date range must include today and last 1 to 365 days',400)
  end
 end
 local revision=(cycle and cycle.revision or 0)+1
 local updated={anchor=periodStart,end_unix=periodEnd,current_days=periodDays,current_period=activePeriod,next_days=r.action=='cycle' and r.days or nextPeriodDays,revision=revision}
 if r.action=='reset' then
  updated.anchor=r.start_unix or now
  updated.end_unix=r.end_unix or now+nextPeriodDays*86400
  updated.current_days=r.end_unix and (r.end_unix-r.start_unix)/86400 or nextPeriodDays
  updated.next_days=nextPeriodDays;updated.current_period='r'..revision..':'..updated.anchor
 end
 local before={period=activePeriod,period_days=periodDays,next_period_days=nextPeriodDays,period_start=periodStart,period_end=periodEnd}
 local after={period=updated.current_period,period_days=updated.current_days,next_period_days=updated.next_days,period_start=updated.anchor,period_end=updated.end_unix}
 local record={ticket_id=r.ticket_id,action=r.action,operator=r.operator,period=activePeriod,occurred_at=r.occurred_at,before=before,after=after}
 local result=cjson.encode({ok=true,period=after.period,cycle=after})
 mark()
 redis.call('SET',p..'cycle_config',cjson.encode(updated))
 redis.call('LPUSH',p..'audit',cjson.encode(record));redis.call('LTRIM',p..'audit',0,9999)
 redis.call('SET',tk,cjson.encode({fingerprint=r.fingerprint,response=result}))
 return result
end
local before={};local after={};local pending=nil;local baseJSON=nil;local tiersJSON=nil;local pricesJSON=nil
if r.action=='prices' then
 before=prices;after=r.prices;pricesJSON=cjson.encode(r.prices)
elseif r.action=='tiers' then
 before=tiers;after=r.tiers;tiersJSON=cjson.encode(r.tiers)
elseif r.dimension=='department' then
 if r.action~='assign' or not tiers[r.tier] then return fail('invalid_action','Department allows assign only',400) end
 before=base('department',r.subject);after={dimension='department',subject=r.subject,tier=r.tier};baseJSON=cjson.encode({tier=r.tier})
else
 if r.dimension~='user' then return fail('invalid_dimension','Only user budgets supported',400) end
 local a=account(r.dimension,r.subject,r.app);before=cjson.decode(cjson.encode(a))
 if r.action=='assign' then
 local b={tier=r.tier,department=r.department or a.department,app=r.app or a.app}
 local t,source=resolved(b);if t=='' then return fail('invalid_department','Valid department required for inheritance',400) end
 baseJSON=cjson.encode(b);a.department=b.department;a.quota_source=source;a.base_tier=r.tier;a.tier=t;a.limit=a.limit+tiers[t].quota-(a.base_quota or 0);a.base_quota=tiers[t].quota;a.models=tiers[t].models or {}
 elseif r.action=='delta' then
 if math.abs(a.limit+r.value)>9999999999999 or a.limit+r.value<0 then return fail('invalid_amount','Resulting limit out of range',400) end;a.limit=a.limit+r.value
 elseif r.action~='refresh' then return fail('invalid_action','Unknown action',400) end
 if a.limit<0 or a.limit>9999999999999 then return fail('invalid_amount','Resulting limit out of range',400) end
 a.quota=a.limit-a.spent;a.balance=a.quota;after=snapshot(a);pending=a
end
local record={ticket_id=r.ticket_id,action=r.action,dimension=r.dimension,subject=r.subject,operator=r.operator,period=r.period,occurred_at=r.occurred_at,before=before,after=after}
local auditJSON=cjson.encode(record)
local result=cjson.encode({ok=true,period=r.period,account=after,tiers_scope='future_initialization'})
local ticketJSON=cjson.encode({fingerprint=r.fingerprint,response=result})
mark()
-- Validate and serialize everything before the first mutation; Redis Lua does not roll back errors.
if pending then local ledgerJSON=cjson.encode(pending);redis.call('HSET',p..'ledger:'..r.period,pending.dimension..':'..pending.subject,ledgerJSON);redis.call('ZADD',p..'registry:'..pending.dimension,0,pending.subject) end
if baseJSON then redis.call('HSET',p..'base',r.dimension..':'..r.subject,baseJSON);redis.call('ZADD',p..'registry:'..r.dimension,0,r.subject) end
if pricesJSON then redis.call('SET',p..'prices',pricesJSON) end
if tiersJSON then redis.call('SET',p..'tiers',tiersJSON) end
redis.call('LPUSH',p..'audit',auditJSON);redis.call('LTRIM',p..'audit',0,9999)
redis.call('SET',tk,ticketJSON)
return result
