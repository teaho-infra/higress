local p=KEYS[1]
local r=cjson.decode(ARGV[1])
local function fail(code,msg,status) return cjson.encode({ok=false,error='ai-quota-walmart.'..code,message=msg,status=status}) end
if redis.call('EXISTS',p..'budget_policy')==1 then return fail('storage_policy_conflict','Money namespace cannot be used as a token ledger',503) end
if r.action~='settle' and r.period_start_unix then
 local now=tonumber(redis.call('TIME')[1]);if now<r.period_start_unix or now>=r.period_end_unix then return fail('period_conflict','Period changed; retry with current period',409) end
end
if r.app~=nil and type(r.app)~='string' then return fail('invalid_request','app must be a string',400) end
local tiers=cjson.decode(redis.call('GET',p..'tiers') or ARGV[2])
local function save(a) redis.call('HSET',p..'ledger:'..r.period,a.dimension..':'..a.subject,cjson.encode(a));redis.call('ZADD',p..'registry:'..a.dimension,0,a.subject) end
local function account(d,s,app)
 local id=d..':'..s
 local base=cjson.decode(redis.call('HGET',p..'base',id) or '{}')
 local a=cjson.decode(redis.call('HGET',p..'ledger:'..r.period,id) or '{}')
 if not a.subject then
 local tier=base.tier or (d=='app' and 'L1' or '')
 local lim=tiers[tier] and tiers[tier].quota or 0
 a={dimension=d,subject=s,app=base.app or app or '',tier=tier,base_tier=tier,limit=lim,base_quota=lim,usage=0,spent=0,requests=0,period=r.period,models=tiers[tier] and tiers[tier].models or {}}
 end
 if app and app~='' then a.app=app end
 a.base_tier=base.tier or (d=='app' and 'L1' or '')
 a.quota=a.limit-a.spent;a.balance=a.quota
 return a
end
local function rank(t) return tonumber(string.sub(t or '',2)) or 0 end
local function snapshot(a)
 a.effective_dimension=a.dimension;a.effective_subject=a.subject;a.effective_tier=a.tier;a.effective_limit=a.limit;a.effective_balance=a.quota
 if a.dimension=='user' and a.app~='' then
 local app=account('app',a.app)
 if rank(a.tier)<rank(app.tier) then a.effective_dimension='app';a.effective_subject=app.subject;a.effective_tier=app.tier;a.effective_limit=app.limit;a.effective_balance=app.quota end
 end
 return a
end
if r.action=='meta' then return cjson.encode({ok=true,period=r.period,period_start=r.period_start,period_end=r.period_end,tiers=tiers,tiers_scope='future_initialization'}) end
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
 local app=account('app',r.app);local user=nil;local payer=app
 if r.subject~='' then user=account('user',r.subject,r.app);if rank(user.tier)>=rank(app.tier) then payer=user end end
 if payer.quota<=0 then return fail('noquota','No quota left',403) end
 if r.model and r.model~='' and #payer.models>0 then local allowed=false;for _,m in ipairs(payer.models) do if m==r.model or m=="*" then allowed=true end end;if not allowed then return fail('model_forbidden','Model not allowed by tier',403) end end
 save(app);if user then save(user) end
 return cjson.encode({ok=true,payer_dimension=payer.dimension,payer_subject=payer.subject,period=r.period})
end
if r.action=='settle' then
 local aa=account('app',r.app);local uu=nil;if r.subject~='' then uu=account('user',r.subject,r.app) end
 if aa.usage+r.value>9999999999999 or (uu and uu.usage+r.value>9999999999999) then return fail('counter_overflow','Usage counter exceeds exact supported range',503) end
 if redis.call('SET',p..'settled:'..r.period..':'..r.ticket_id,'1','NX','EX',5184000)==false then return cjson.encode({ok=true}) end
 local app=account('app',r.app);app.usage=app.usage+r.value;app.requests=app.requests+1;if r.payer_dimension=='app' then app.spent=app.spent+r.value end;save(app)
 if r.subject~='' then local u=account('user',r.subject,r.app);u.usage=u.usage+r.value;u.requests=u.requests+1;if r.payer_dimension=='user' then u.spent=u.spent+r.value end;save(u) end
 return cjson.encode({ok=true})
end
if r.action=='query' or r.action=='usage' then local a=account(r.dimension,r.subject);a.ok=true;return cjson.encode(snapshot(a)) end
local tk=p..'ticket:'..r.ticket_id
local prior=redis.call('GET',tk)
if prior then local old=cjson.decode(prior);if old.fingerprint~=r.fingerprint then return fail('ticket_conflict','Ticket already used with different request',409) end;return old.response end
local before={};local after={};local pending=nil;local baseJSON=nil;local tiersJSON=nil
if r.action=='tiers' then
 before=tiers;after=r.tiers;tiersJSON=cjson.encode(r.tiers)
else
 local a=account(r.dimension,r.subject,r.app);before=cjson.decode(cjson.encode(a))
 if r.action=='assign' then
 baseJSON=cjson.encode({tier=r.tier,app=r.app or a.app});a.base_tier=r.tier;a.tier=r.tier;a.limit=a.limit+tiers[r.tier].quota-(a.base_quota or 0);a.base_quota=tiers[r.tier].quota;a.models=tiers[r.tier].models or {}
 elseif r.action=='tier' then
 if rank(r.tier)<rank(a.tier) then return fail('downgrade_forbidden','Temporary tier only allows upgrade',409) end
 local old=a.base_quota or 0;a.limit=a.limit+tiers[r.tier].quota-old;a.base_quota=tiers[r.tier].quota;a.tier=r.tier;a.models=tiers[r.tier].models or {}
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
-- Validate and serialize everything before the first mutation; Redis Lua does not roll back errors.
if pending then local ledgerJSON=cjson.encode(pending);redis.call('HSET',p..'ledger:'..r.period,pending.dimension..':'..pending.subject,ledgerJSON);redis.call('ZADD',p..'registry:'..pending.dimension,0,pending.subject) end
if baseJSON then redis.call('HSET',p..'base',r.dimension..':'..r.subject,baseJSON) end
if tiersJSON then redis.call('SET',p..'tiers',tiersJSON) end
redis.call('LPUSH',p..'audit',auditJSON);redis.call('LTRIM',p..'audit',0,9999)
redis.call('SET',tk,ticketJSON)
return result
