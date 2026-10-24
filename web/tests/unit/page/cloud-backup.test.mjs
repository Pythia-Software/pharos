import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, statSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { delimiter, join } from 'node:path';
import { loadClassicScript, plain } from '../helpers/classic-script.mjs';

const cloud = loadClassicScript('cloud-backup.js').window.pharosCloudBackup;
const close = (actual, expected) => assert.ok(Math.abs(actual - expected) < 1e-8, `${actual} != ${expected}`);
const configured = extra => ({...cloud.defaults,provider:'aws',region:'us-east-1',tier:'aws-standard',compression:100,project:'pharos-example',subscription:'Backup subscription',bucket:'my-exact-backup-bucket',...extra});

test('new cloud plans default to GCP Standard in Iowa', () => {
  const p=cloud.normalize(cloud.defaults);
  assert.equal(p.provider,'gcp');
  assert.equal(p.region,'us-central1');
  assert.equal(p.tier,'gcp-standard');
});

test('calendar periods cross years and leap days using actual month boundaries', () => {
  const crossing=cloud.calendarPeriods('2026-12-31',2);
  assert.deepEqual(plain(crossing.map(p=>[p.label,p.startDay,p.endDay])),[['Dec 2026',0,1],['Jan 2027',1,2]]);
  const leap=cloud.calendarPeriods('2024-02-28',3);
  assert.deepEqual(plain(leap.map(p=>[p.label,p.startDay,p.endDay])),[['Feb 2024',0,2],['Mar 2024',2,3]]);
  const forecast=cloud.forecast(configured(),undefined,'2026-10-23');
  assert.equal(forecast.months[0].label,'Oct 2026');
  assert.equal(forecast.months[0].days,9);
  assert.equal(forecast.months.at(-1).label,'Oct 2027');
  assert.equal(forecast.months.at(-1).days,22);
  assert.equal(forecast.firstMonthLabel,'Nov 2026');
  close(forecast.firstMonth,forecast.months[1].cost);
  for(const [start,label,days]of [['2026-04-01','May 2026',31],['2026-12-31','Jan 2027',31],['2024-01-31','Feb 2024',29]]){
    const next=cloud.forecast(configured(),undefined,start);
    assert.equal(next.firstMonthLabel,label);
    assert.equal(next.months[1].days,days);
    close(next.firstMonth,next.months[1].cost);
  }
  close(forecast.months.reduce((sum,m)=>sum+m.cost,0),forecast.year);
  for(const month of forecast.months)close(month.cost,month.storageCost+month.uploadCost+month.minimumStorageCost);
  assert.equal(forecast.months.reduce((sum,m)=>sum+m.days,0),365);
});

test('default compression is fixed at 30%, rounded from the library sample', () => {
  const measurement=cloud.compressionMeasurement;
  assert.ok(measurement.uncompressed_bytes>measurement.compressed_bytes);
  close(cloud.defaults.compression,30);
  close(measurement.compressed_percent,measurement.compressed_bytes/measurement.uncompressed_bytes*100);
  assert.match(measurement.method,/SQLite online snapshot/);
});

test('growth is linear, uses the measured 90-day rate and falls back to a fixed 5% monthly amount', () => {
  const fallback=cloud.normalize(configured({sizeGB:100}));
  assert.equal(fallback.growthGBPerMonth,5);
  close(cloud.forecast(fallback).libraryYearBytes,(100+5*365/30)*1e9);
  const measured=cloud.normalize(configured({sizeGB:100,growthBasis:'measured_90_days',growthGBPerMonth:12,historyDays:90}));
  assert.equal(measured.growthGBPerMonth,12);
  close(cloud.forecast(measured).libraryYearBytes,246e9);
  assert.equal(cloud.normalize({...measured,growthGBPerMonth:-5}).growthGBPerMonth,0);
});

test('first-year pricing includes startup, growing snapshots and retention instead of twelve flat months', () => {
  const input=configured({provider:'gcp',tier:'gcp-standard',sizeGB:100,compression:50,keep:1,growthBasis:'measured_90_days',growthGBPerMonth:0});
  const flat=cloud.forecast(input,undefined,'2026-10-23'),growing=cloud.forecast({...input,growthGBPerMonth:10},undefined,'2026-10-23');
  // November: 30 current-version days, four extra old-version days during
  // the pruning grace period, and four uploads on days 14, 21, 28 and 35.
  close(flat.firstMonth,50e9/2**30*.02*34/30+4*.005/1000);
  assert.ok(growing.year>flat.year);
  assert.ok(growing.month12>growing.firstMonth);
  close(growing.year,growing.daily.reduce((a,b)=>a+b,0));
  assert.notEqual(growing.year,growing.firstMonth*12);
  const noRetention=cloud.forecast({...input,keep:100});
  assert.ok(noRetention.year>flat.year);
  const cold=cloud.forecast({...input,tier:'gcp-archive'});
  assert.ok(cold.deletionCharges>0);
  close(cold.minimumStorageCost,cold.deletionCharges);
  // Count, grace period and time as the current revision all contribute to
  // the age at pruning; a cold class alone does not imply penalties.
  const nearline={...input,tier:'gcp-nearline',keep:4};
  assert.equal(cloud.forecast(nearline).earliestPruneAgeDays,28);
  assert.ok(cloud.forecast(nearline).deletionCharges>0);
  assert.equal(cloud.forecast({...nearline,keep:5}).deletionCharges,0);
  assert.equal(cloud.forecast({...nearline,minimumAge:23}).deletionCharges,0);
  assert.ok(cloud.forecast({...nearline,retentionMode:'days',retentionDays:22}).deletionCharges>0);
  assert.equal(cloud.forecast({...nearline,retentionMode:'days',retentionDays:23}).deletionCharges,0);
});

test('calendar cost breakdown separates upload frequency from storage and minimum-duration charges', () => {
  for(const provider of ['aws','gcp','azure']){
    const input=configured({provider,sizeGB:100,compression:50,growthBasis:'custom',growthGBPerMonth:0});
    const weekly=cloud.forecast(input,undefined,'2026-04-01'),daily=cloud.forecast({...input,cadence:1},undefined,'2026-04-01');
    assert.ok(daily.uploadCost>weekly.uploadCost);
    for(const forecast of [weekly,daily]){
      assert.ok(forecast.storageCost>0&&forecast.uploadCost>0);
      assert.equal(forecast.minimumStorageCost,0);
      close(forecast.year,forecast.storageCost+forecast.uploadCost+forecast.minimumStorageCost);
      for(const key of ['storageCost','uploadCost','minimumStorageCost'])close(forecast.months.reduce((sum,m)=>sum+m[key],0),forecast[key]);
      for(const month of forecast.months)close(month.cost,month.storageCost+month.uploadCost+month.minimumStorageCost);
    }
    if(provider==='gcp'){
      close(weekly.months[0].uploadCost,5*.005/1000);
      close(daily.months[0].uploadCost,30*.005/1000);
    }
  }
  const archive=cloud.forecast(configured({tier:'aws-deep'}),undefined,'2026-10-23');
  assert.ok(archive.minimumStorageCost>archive.uploadCost);
  close(archive.year,archive.storageCost+archive.uploadCost+archive.minimumStorageCost);
  for(const month of archive.months)close(month.cost,month.storageCost+month.uploadCost+month.minimumStorageCost);
});

test('small upload fees remain distinguishable instead of rounding to zero dollars', () => {
  assert.equal(cloud.formatCost(0),'$0.00');
  assert.equal(cloud.formatCost(.000005),'$0.000005');
  assert.equal(cloud.formatCost(.000025),'$0.000025');
  assert.equal(cloud.formatCost(.00015),'$0.00015');
  assert.equal(cloud.formatCost(.0000001),'<$0.000001');
  assert.equal(cloud.formatCost(1.23),'$1.23');
});

test('regional forecast ranges cover every offered location and calculator needs no account details', () => {
  const p=configured({provider:'gcp',region:'us-central1',project:''}),range=cloud.priceRange(p,'gcp-standard');
  for(const region of cloud.regions.gcp){const e=cloud.forecast({...p,region:region.id},'gcp-standard');for(const key of ['firstMonth','year','month12'])assert.ok(e[key]>=range[key].min&&e[key]<=range[key].max);}
  assert.deepEqual(plain(cloud.validation(p,0)),{});
  assert.ok(cloud.validation(p,2).project);
  assert.ok(cloud.validation({...p,keep:0},0).keep);
});

test('chart series compare a growing library with an explicitly flat baseline', () => {
  const input=configured({provider:'gcp',region:'us-central1',tier:'gcp-standard',sizeGB:100,compression:50,keep:1});
  const {projected,flat}=cloud.projection(input,'2026-04-01');
  assert.equal(projected.months.length,12);
  assert.equal(projected.storedBytes.length,365);
  close(flat.libraryYearBytes,100e9);
  assert.ok(projected.libraryYearBytes>flat.libraryYearBytes);
  close(flat.months[0].storedGB,50*34/30);
  close(projected.months.reduce((sum,m)=>sum+m.cost,0),projected.year);
  assert.equal(projected.months.reduce((sum,m)=>sum+m.days,0),365);
  close(projected.months[1].cost,projected.firstMonth);
  for(let i=0;i<12;i++){
    assert.ok(projected.months[i].storedGB>=flat.months[i].storedGB);
    assert.ok(projected.months[i].cost>=flat.months[i].cost);
  }
  const zero=cloud.projection({...input,growthBasis:'custom',growthGBPerMonth:0});
  assert.deepEqual(plain(zero.projected.months),plain(zero.flat.months));
});

test('detailed calculator choices survive normalization and exported setup settings', () => {
  const p=configured({region:'eu-central-1',tier:'aws-ia',cadence:1,retentionMode:'days',retentionDays:45,growthBasis:'custom',growthGBPerMonth:12,compression:40,partMiB:128});
  const exported=cloud.destination(p);
  assert.equal(exported.region,p.region);
  assert.equal(exported.storage_class,'STANDARD_IA');
  assert.equal(exported.cadence,1);
  assert.equal(exported.retentionDays,45);
  assert.equal(exported.growthBasis,'custom');
  assert.equal(exported.growthGBPerMonth,12);
  assert.equal(exported.compression,40);
  assert.equal(exported.effective_part_mib,64);
  assert.ok(cloud.validation({...p,growthGBPerMonth:''},0).growthGBPerMonth);
  assert.ok(cloud.validation({...p,region:'unknown'},0).region);
});

test('setup defaults are full archives with database credentials and cloud-account restore', () => {
  assert.deepEqual(plain(cloud.security), {credential_storage:'pharos_database',encryption:'provider_managed',restore_auth:'cloud_account',interactive_upload:false});
  const config = cloud.destination(configured());
  assert.equal(config.format, 'full_archive_zstd');
  assert.equal(config.versioning, true);
  assert.equal(config.schedule_enabled, false);
  assert.equal(config.credential_storage, 'pharos_database');
  assert.equal(config.object, 'pharos/library.tar.zst');
  assert.equal(config.upload_bandwidth_limit_mbps,null);
  assert.match(cloud.prompt(configured()), /without an archive password or a separate recovery key/);
});

test('optional password protection exports encryption-only recovery settings and separate setup stages', () => {
  for(const provider of ['aws','gcp','azure']){
    const input=configured({provider,region:cloud.regions[provider][0].id,tier:cloud.tiers.find(t=>t.provider===provider).id,passwordProtected:true}),stages=cloud.setupStages(input),config=cloud.destination(input);
    assert.deepEqual(plain(stages.map(stage=>stage.id)),['before-password','password','after-password']);
    assert.equal(config.encryption,'age_public_key');assert.equal(config.restore_auth,'cloud_account_and_password');
    assert.equal(config.include_recovery_bundle,true);assert.equal(config.recovery_bundle_file,'recovery-bundle.json');
    assert.equal(config.encryption_setup,'pending');assert.throws(()=>cloud.setup(input),/separate stages/);
    assert.doesNotMatch(stages[0].commands,/\n(?:gcloud|aws|az) /);
    assert.match(stages[1].commands,/^python3 .*setup-password\.py" <\/dev\/tty\n$/);
    assert.match(stages[2].commands,/encryption_setup.*verified/);
    assert.match(stages[2].commands,/embed recovery-bundle.json outside the encrypted payload in EVERY completed backup/);
    for(const stage of stages){const body=stage.commands.replace(/^bash <<'PHAROS_CLOUD_SETUP'\n/,'').replace(/\nPHAROS_CLOUD_SETUP\n$/,'');const result=spawnSync('/bin/bash',['-n'],{input:body,encoding:'utf8'});assert.equal(result.status,0,result.stderr);}
  }
  assert.equal(cloud.normalize({passwordProtected:'true'}).passwordProtected,true);
  assert.equal(cloud.normalize({passwordProtected:'false'}).passwordProtected,false);
  assert.ok(cloud.validation(configured({passwordProtected:'unknown'})).passwordProtected);
  assert.doesNotMatch(JSON.stringify(cloud.normalize(configured({passwordProtected:true,password:'discard-this-fixture',privateKey:'discard-this-too'}))),/discard-this/);
  assert.match(cloud.prompt(configured({passwordProtected:true})),/two independently copyable Bash blocks/);
  assert.doesNotMatch(cloud.prompt(configured({passwordProtected:true})),/without an archive password/);
});

function passwordFixture(t){
  const root=mkdtempSync(join(tmpdir(),'pharos-password-'));t.after(()=>rmSync(root,{recursive:true,force:true}));
  const input=configured({passwordProtected:true}),stages=cloud.setupStages(input);
  const helper=stages[0].commands.match(/<<'PHAROS_PASSWORD_HELPER'\n([\s\S]*?)PHAROS_PASSWORD_HELPER/)[1];
  writeFileSync(join(root,'setup-password.py'),helper,{mode:0o600});
  writeFileSync(join(root,'destination-draft.json'),JSON.stringify(cloud.destination(input)),{mode:0o600});
  return{root,input,stages};
}

test('final protected setup refuses missing, incomplete or mismatched recovery state before cloud commands', t => {
  const {root,input,stages}=passwordFixture(t),guard=stages[2].commands.match(/python3 - <<'PY'\n([\s\S]*?)\nPY/)[1];
  const check=()=>spawnSync('python3',['-c',guard],{encoding:'utf8',env:{...process.env,PHAROS_SETUP:root}});
  assert.notEqual(check().status,0);
  const bundle={version:1,format:'age-password-recovery-v1',recipient:'age1-test-fixture',encrypted_identity:'-----BEGIN AGE ENCRYPTED FILE-----\ntest-fixture'};
  writeFileSync(join(root,'recovery-bundle.json'),JSON.stringify(bundle));
  assert.notEqual(check().status,0);
  const draft={...cloud.destination(input),encryption_setup:'verified',public_recipient:bundle.recipient};
  writeFileSync(join(root,'destination-draft.json'),JSON.stringify(draft));assert.equal(check().status,0);
  writeFileSync(join(root,'destination-draft.json'),JSON.stringify({...draft,region:'eu-central-1'}));assert.notEqual(check().status,0);
  writeFileSync(join(root,'destination-draft.json'),JSON.stringify({...draft,public_recipient:'age1-another-fixture'}));assert.notEqual(check().status,0);
});

test('password helper requires a controlling terminal instead of consuming stdin as a password', t => {
  const {root}=passwordFixture(t);
  const result=spawnSync('python3',['-c','import os,runpy; os.setsid(); runpy.run_path(os.environ["PHAROS_PASSWORD_HELPER"],run_name="__main__")'],{input:'queued shell commands\n',encoding:'utf8',env:{...process.env,PHAROS_PASSWORD_HELPER:join(root,'setup-password.py')}});
  assert.notEqual(result.status,0);assert.equal(JSON.parse(readFileSync(join(root,'destination-draft.json'))).encryption_setup,'pending');
});

const agePath=process.env.PHAROS_TEST_AGE_BIN_DIR?process.env.PHAROS_TEST_AGE_BIN_DIR+delimiter+process.env.PATH:process.env.PATH;
const ageAvailable=spawnSync('age',['--version'],{env:{...process.env,PATH:agePath}}).status===0;
for(const mode of ['success','wrong-confirmation','wrong-verification'])test(`password helper keeps only encrypted recovery material: ${mode}`, {skip:ageAvailable?false:'Install age or set PHAROS_TEST_AGE_BIN_DIR to run the real-crypto terminal checks.'}, t => {
  const {root}=passwordFixture(t);
  const driver=String.raw`import errno, json, os, pty, select, signal, sys, time
root, mode = os.environ['PHAROS_PASSWORD_ROOT'], os.environ['PHAROS_PASSWORD_MODE']
password = b'pharos fixture password for terminal checks'
replies = [password, b'wrong fixture' if mode == 'wrong-confirmation' else password, b'wrong fixture' if mode == 'wrong-verification' else password]
prompts = [b'Enter passphrase (leave empty to autogenerate a secure one):', b'Confirm passphrase:', b'Enter passphrase:']
pid, fd = pty.fork()
if pid == 0:
    os.execv(sys.executable, [sys.executable, root+'/setup-password.py'])
pending, transcript, sent = b'', b'', 0
deadline = time.monotonic()+45
try:
    while time.monotonic() < deadline:
        if not select.select([fd], [], [], 1)[0]:
            continue
        try:
            chunk = os.read(fd, 65536)
        except OSError as error:
            if error.errno == errno.EIO:
                break
            raise
        if not chunk:
            break
        transcript += chunk
        pending += chunk
        if sent < len(prompts) and prompts[sent] in pending:
            pending = pending.split(prompts[sent], 1)[1]
            os.write(fd, replies[sent]+b'\n')
            sent += 1
    else:
        os.kill(pid, signal.SIGKILL)
        raise RuntimeError('Terminal helper did not reach the expected prompts')
finally:
    os.close(fd)
_, status = os.waitpid(pid, 0)
print(json.dumps({'status':os.waitstatus_to_exitcode(status),'prompts':sent,'password_leaked':password in transcript,'private_key_leaked':b'AGE-SECRET-KEY-' in transcript}))
`;
  const result=spawnSync('python3',['-c',driver],{encoding:'utf8',timeout:55000,env:{...process.env,PATH:agePath,PHAROS_PASSWORD_ROOT:root,PHAROS_PASSWORD_MODE:mode}});
  assert.equal(result.status,0,result.stderr);const receipt=JSON.parse(result.stdout);
  assert.equal(receipt.password_leaked,false);assert.equal(receipt.private_key_leaked,false);
  const draft=JSON.parse(readFileSync(join(root,'destination-draft.json')));
  if(mode!=='success'){assert.equal(receipt.prompts,mode==='wrong-confirmation'?2:3);assert.notEqual(receipt.status,0);assert.equal(draft.encryption_setup,'pending');assert.throws(()=>readFileSync(join(root,'recovery-bundle.json')));return;}
  assert.equal(receipt.status,0);assert.equal(receipt.prompts,3);assert.equal(draft.encryption_setup,'verified');
  const bundle=JSON.parse(readFileSync(join(root,'recovery-bundle.json')));
  for(const file of ['destination-draft.json','recovery-bundle.json'])assert.equal(statSync(join(root,file)).mode&0o777,0o600);
  assert.equal(bundle.recipient,draft.public_recipient);assert.match(bundle.encrypted_identity,/BEGIN AGE ENCRYPTED FILE/);
  assert.doesNotMatch(JSON.stringify({draft,bundle}),/AGE-SECRET-KEY-|pharos fixture password/);
});

test('140 decimal GB, four weekly full snapshots and optional compression drive capacity and cost', () => {
  const result = cloud.estimate(configured());
  assert.equal(result.snapshotBytes, 140e9);
  assert.equal(result.liveBytes, 560e9);
  assert.equal(result.monthlyBytes, 600e9);
  close(result.storageCost, 560e9 / 2 ** 30 * .023);
  const compressed = cloud.estimate(configured({compression:50}));
  assert.equal(compressed.snapshotBytes, 70e9);
  close(compressed.storageCost, result.storageCost / 2);
  close(compressed.monthlyBytes, result.monthlyBytes / 2);
});

test('GCP can create a new project and link billing before provisioning the backup', () => {
  const input=configured({provider:'gcp',region:'us-central1',tier:'gcp-standard',projectMode:'new',billingAccount:'012ABC-345DEF-678ABC'});
  assert.match(cloud.prompt(input),/Create project pharos-example.*link billing account 012ABC-345DEF-678ABC/);
  for(const [projectParent,flag]of [['',''],['organizations/123456',"--organization '123456'"],['folders/987654',"--folder '987654'"]]){
    const p={...input,projectParent},script=cloud.setup(p),config=cloud.destination(p);
    assert.deepEqual(plain(cloud.validation(p)),{});
    assert.equal(config.projectMode,'new');assert.equal(config.billingAccount,input.billingAccount);assert.equal(config.projectParent,projectParent);
    const create=script.indexOf("gcloud projects create 'pharos-example'"),billing=script.indexOf("gcloud billing projects link 'pharos-example' --billing-account '012ABC-345DEF-678ABC'"),enable=script.indexOf('gcloud services enable storage.googleapis.com iam.googleapis.com'),bucket=script.indexOf('gcloud storage buckets create');
    assert.ok(create>=0&&billing>create&&enable>billing&&bucket>enable);
    if(flag)assert.ok(script.includes(flag));else assert.doesNotMatch(script,/--organization|--folder/);
    const body=script.replace(/^bash <<'PHAROS_CLOUD_SETUP'\n/,'').replace(/\nPHAROS_CLOUD_SETUP\n$/,'');
    const bash=spawnSync('/bin/bash',['-n'],{input:body,encoding:'utf8'});assert.equal(bash.status,0,bash.stderr);
  }
  const existing={...input,projectMode:'existing',billingAccount:'',projectParent:''};
  assert.deepEqual(plain(cloud.validation(existing)),{});
  assert.doesNotMatch(cloud.setup(existing),/gcloud projects create|gcloud billing projects link/);
  assert.equal(cloud.normalize({provider:'gcp'}).projectMode,'existing');
  for(const extra of [{billingAccount:''},{billingAccount:"012ABC-345DEF-678ABC;id"},{projectParent:'123456'},{projectParent:'folders/12;id'},{projectMode:'unknown'},{project:"bad$(id)"}]){
    const p={...input,...extra};assert.ok(Object.keys(cloud.validation(p)).length);assert.throws(()=>cloud.setup(p));
  }
  assert.deepEqual(plain(cloud.validation({...input,billingAccount:''},0)),{});
});

test('location and class use regional capacity, request and retrieval prices', () => {
  assert.equal(cloud.rate(configured({region:'eu-central-1'})).storage, .0245);
  assert.ok(cloud.estimate(configured({region:'eu-central-1'})).monthly > cloud.estimate(configured()).monthly);
  const london = configured({provider:'gcp',tier:'gcp-archive',region:'europe-west2'});
  assert.equal(cloud.rate(london).storage, .0025);
  assert.equal(cloud.rate({...london,region:'us-central1'}).storage, .0012);
  assert.equal(cloud.rate(configured({tier:'aws-deep',region:'eu-central-1'})).retrieval, .024);
  assert.equal(cloud.rate(configured({provider:'azure',tier:'azure-hot',region:'westus2'})).storage, .0184);
});

test('a resumable GCP archive has one billable write even when HTTP parts are small', () => {
  const input = configured({provider:'gcp',region:'us-central1',tier:'gcp-standard'});
  const result = cloud.estimate(input);
  close(result.monthlyWrites, 30 / 7);
  close(result.writesCost, 30 / 7 * .005 / 1000);
  close(cloud.estimate({...input,partMiB:512}).writesCost, result.writesCost);
  assert.ok(cloud.estimate(configured()).monthlyWrites > result.monthlyWrites);
  const small = cloud.estimate(configured({sizeGB:.01}));
  close(small.monthlyWrites,30/7);
  close(small.writesCost,30/7*.005/1000);
});

test('minimum age can keep extra revisions and archive minimum billing can exceed live storage', () => {
  const young = cloud.estimate(configured({keep:1,cadence:1,minimumAge:10}));
  assert.equal(young.count, 11);
  const deep = cloud.estimate(configured({tier:'aws-deep'}));
  assert.equal(deep.count, 4);
  assert.ok(deep.billedCount > deep.count);
  assert.ok(deep.earlyDeletion > 0 && deep.metadataCost > 0);
  const long = cloud.estimate(configured({tier:'aws-deep',keep:30}));
  assert.equal(long.earlyDeletion, 0);
  close(deep.monthly, deep.storageCost + deep.earlyDeletion + deep.metadataCost + deep.writesCost);
});

test('age retention keeps the current archive and Azure cannot silently select count retention', () => {
  const input = configured({retentionMode:'days',retentionDays:30,cadence:7});
  assert.equal(cloud.estimate(input).count, 6);
  assert.equal(cloud.normalize({...input,provider:'azure',retentionMode:'count'}).retentionMode,'days');
  assert.equal(cloud.estimate({...input,provider:'azure'}).count,5);
  const rule = cloud.lifecycle({...input,provider:'azure',object:'chosen/archive.tar.zst'}).rules[0];
  assert.equal(rule.definition.actions.version.delete.daysAfterCreationGreaterThan,30);
  assert.equal(rule.definition.actions.baseBlob,undefined);
  assert.deepEqual(plain(rule.definition.filters.prefixMatch),['pharos/chosen/archive.tar.zst']);
});

test('native version lifecycle counts whole completed snapshots and never expires the current revision', () => {
  const aws = cloud.lifecycle(configured({keep:4,minimumAge:3})).Rules[0];
  assert.deepEqual(plain(aws.NoncurrentVersionExpiration),{NoncurrentDays:3,NewerNoncurrentVersions:3});
  assert.equal(aws.Expiration,undefined);
  const one = cloud.lifecycle(configured({keep:1})).Rules[0];
  assert.equal(one.NoncurrentVersionExpiration.NewerNoncurrentVersions,undefined);
  const gcp = cloud.lifecycle(configured({provider:'gcp',keep:4,minimumAge:3})).rule[0].condition;
  assert.equal(gcp.isLive,false);
  assert.equal(gcp.numNewerVersions,4);
  assert.equal(gcp.daysSinceNoncurrentTime,3);
  assert.equal(gcp.age,undefined);
});

test('restore retrieval and editable egress use the compressed archive size', () => {
  const result = cloud.estimate(configured({provider:'gcp',tier:'gcp-archive',region:'us-central1',compression:50}));
  close(result.retrievalCost, 70e9 / 2 ** 30 * .05);
  close(result.egressCost, 8.4);
  assert.equal(cloud.estimate(configured({egress:0})).egressCost,0);
});

test('invalid values cannot generate runnable commands or hidden fallback regions', () => {
  for(const extra of [{bucket:"abc';touch /tmp/injected;#"},{bucket:'a$(id)'},{bucket:'ab'},{region:'unknown-region'},{provider:'other'},{project:"foo';id;#",provider:'gcp'},
    {provider:'gcp',project:''},{object:'../outside/archive.tar.zst'},{time:'25:00'},{timezone:'bad/timezone'},{compression:0},{sizeGB:Infinity},{keep:1.2},{sizeGB:1000000},{name:'label\ncommand'},{provider:'azure',tier:'azure-archive'}]) {
    const input = configured(extra);
    assert.ok(Object.keys(cloud.validation(input)).length, JSON.stringify(extra));
    assert.throws(()=>cloud.setup(input));
    assert.ok(Number.isFinite(cloud.estimate(input).monthly));
  }
});

test('all priced locations produce valid commands for supported classes and reject Azure Archive', () => {
  for(const [provider,regions] of Object.entries(cloud.regions)) for(const region of regions) for(const tier of cloud.tiers.filter(t=>t.provider===provider)) {
    const input=configured({provider,region:region.id,tier:tier.id,name:"My team's off-site copy"});
    if(tier.id==='azure-archive'){assert.throws(()=>cloud.setup(input),/cannot overwrite/);continue;}
    const script=cloud.setup(input);
    assert.match(script,/^bash <<'PHAROS_CLOUD_SETUP'\n/);
    const body=script.replace(/^bash <<'PHAROS_CLOUD_SETUP'\n/,'').replace(/\nPHAROS_CLOUD_SETUP\n$/,'');
    const bash=spawnSync('/bin/bash',['-n'],{input:body,encoding:'utf8'});
    assert.equal(bash.status,0,`${tier.id}/${region.id}: ${bash.stderr}`);
    assert.ok(script.includes(region.id));
    assert.ok(script.includes(provider==='azure'?input.account:input.bucket));
    assert.doesNotMatch(script,/openssl rand|If-None-Match|if-none-match/);
    for(const match of script.matchAll(/python3 - <<'PY'\n([\s\S]*?)\nPY/g)) {
      const python=spawnSync('python3',['-c','import ast,sys; ast.parse(sys.stdin.read())'],{input:match[1],encoding:'utf8'});
      assert.equal(python.status,0,`${tier.id}/${region.id}: ${python.stderr}`);
    }
  }
});

test('generated policy/configuration JSON agrees with every reviewed setup choice', t => {
  for(const provider of ['aws','gcp','azure']) {
    const root=mkdtempSync(join(tmpdir(),'pharos-cloud-'));
    t.after(()=>rmSync(root,{recursive:true,force:true}));
    const region=cloud.regions[provider][3].id;
    const input=configured({provider,region,keep:7,cadence:1,time:'04:35',weekday:2,timezone:'Europe/London',object:'backups/selected.tar.zst',identity:'chosen-uploader',partMiB:128,compression:60,tokenDays:180});
    const script=cloud.setup(input);
    writeFileSync(join(root,'upload.sas'),'test-only-sas');
    // Execute only generated Python serialization, never cloud/shell commands.
    for(const match of script.matchAll(/python3 - <<'PY'\n([\s\S]*?)\nPY/g)) {
      const result=spawnSync('python3',['-c',match[1]],{encoding:'utf8',env:{...process.env,PHAROS_SETUP:root}});
      assert.equal(result.status,0,result.stderr);
    }
    const config=JSON.parse(readFileSync(join(root,'destination-draft.json'),'utf8'));
    assert.equal(config.region,region);
    assert.equal(config.object,input.object);
    assert.equal(config.identity,input.identity);
    assert.equal(config.keep,7);
    assert.equal(config.time,'04:35');
    assert.equal(config.timezone,'Europe/London');
    assert.equal(config.credential_storage,'pharos_database');
    assert.deepEqual(JSON.parse(readFileSync(join(root,'lifecycle.json'),'utf8')),plain(cloud.lifecycle(input)));
    if(provider==='aws') {
      const policy=JSON.parse(readFileSync(join(root,'uploader-policy.json'),'utf8'));
      assert.deepEqual(policy.Statement[0].Action,['s3:PutObject','s3:AbortMultipartUpload']);
      assert.deepEqual(policy.Statement[0].Resource,['arn:aws:s3:::my-exact-backup-bucket/backups/selected.tar.zst','arn:aws:s3:::my-exact-backup-bucket/backups/selected.tar.zst.roundtrip-test']);
      assert.ok(policy.Statement[1].Action.includes('s3:Get*'));
      assert.ok(script.includes("'LocationConstraint=eu-central-1'"));
    } else if(provider==='gcp') {
      assert.deepEqual(script.match(/--permissions ([^\s]+)/)[1].split(',').sort(),['storage.objects.create','storage.objects.delete']);
      assert.match(script,/--versioning --clear-soft-delete/);
      assert.match(script,/chosen-uploader@pharos-example\.iam\.gserviceaccount\.com/);
    } else {
      assert.match(script,/--permissions cw/);
      assert.match(script,/timedelta\(days=180\)/);
      assert.match(script,/--enable-versioning true/);
    }
  }
});

test('Azure sample and full recovery downloads use the selected subscription', t => {
  const root=mkdtempSync(join(tmpdir(),'pharos-azure-download-'));
  t.after(()=>rmSync(root,{recursive:true,force:true}));
  const argsPath=join(root,'azure-args');
  writeFileSync(join(root,'az'),`#!/bin/bash
printf '%s\\0' "$@" > "$PHAROS_AZ_ARGS"
while [ "$#" -gt 0 ]; do
  if [ "$1" = '--file' ]; then
    printf '%s' 'download fixture' > "$2"
    exit 0
  fi
  shift
done
exit 1
`,{mode:0o700});
  for(const kind of ['sample','full'])for(const passwordProtected of [false,true]){
    const input=configured({provider:'azure',region:'eastus',tier:'azure-hot',subscription:'Selected tenant backup subscription',passwordProtected});
    const receipt={kind,version:'2026-10-23T18:00:00.0000000Z',object:input.object+(kind==='sample'?'.roundtrip-test':'')};
    const folder=join(root,kind+'-'+passwordProtected);
    const result=spawnSync('/bin/bash',[],{input:cloud.restoreDownload(input,receipt,folder),encoding:'utf8',env:{...process.env,PATH:root+delimiter+process.env.PATH,PHAROS_AZ_ARGS:argsPath}});
    assert.equal(result.status,0,result.stderr);
    const args=readFileSync(argsPath,'utf8').split('\0');
    for(const [flag,value]of [['--subscription',input.subscription],['--auth-mode','login'],['--account-name',input.account],['--container-name',input.container],['--name',receipt.object],['--version-id',receipt.version]]){
      assert.ok(args.includes(flag),`missing ${flag}`);
      assert.equal(args[args.indexOf(flag)+1],value);
    }
    const file=kind==='sample'?'round-trip-test.pharos':passwordProtected?'library.pharos':'library.tar.zst';
    assert.equal(readFileSync(join(folder,file),'utf8'),'download fixture');
  }
});
