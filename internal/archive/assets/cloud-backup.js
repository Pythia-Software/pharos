// Cloud backup planner and live operation UI. Credentials are read server-side.
// Pure planning functions are shared with node tests.
(() => {
  'use strict';
  const GB = 1e9, GiB = 2 ** 30, uploadPartMiB = 64, reviewed = '2026-10-22';
  const formatCost = value => value > 0 && value < .000001 ? '<$0.000001' : new Intl.NumberFormat('en-US',{style:'currency',currency:'USD',minimumFractionDigits:2,maximumFractionDigits:value>0&&value<.01?6:2}).format(value);
  const providers = {
    aws: {name: 'Amazon S3', source: 'https://aws.amazon.com/s3/pricing/', permissions: 'Upload and resume this archive; no download, object listing or deletion of completed revisions.'},
    gcp: {name: 'Google Cloud Storage', source: 'https://cloud.google.com/storage/pricing', permissions: 'Create and delete objects in this bucket; no download or object listing.'},
    azure: {name: 'Azure Blob Storage', source: 'https://azure.microsoft.com/en-us/pricing/details/storage/blobs/', permissions: 'Create and overwrite blobs in this container; no download, listing or deletion.'},
  };
  const regions = {
    aws: [
      {"id":"us-east-1","label":"US · Northern Virginia","retrieval":[0,0.01,0.03,0.02],"storage":[0.023,0.0125,0.004,0.00099],"write":[0.005,0.01,0.02,0.05],"multipart":{"UploadPart":[0.005,0.01,0.02,0.005],"InitiateMultipartUpload":[0.005,0.01,0.02,0.005],"CompleteMultipartUpload":[0.005,0.01,0.02,0.05]}},
      {"id":"us-west-2","label":"US · Oregon","retrieval":[0,0.01,0.03,0.02],"storage":[0.023,0.0125,0.004,0.00099],"write":[0.005,0.01,0.02,0.05],"multipart":{"UploadPart":[0.005,0.01,0.02,0.005],"InitiateMultipartUpload":[0.005,0.01,0.02,0.005],"CompleteMultipartUpload":[0.005,0.01,0.02,0.05]}},
      {"id":"eu-west-1","label":"Europe · Ireland","retrieval":[0,0.01,0.03,0.02],"storage":[0.023,0.0125,0.004,0.00099],"write":[0.005,0.01,0.02,0.055],"multipart":{"UploadPart":[0.005,0.01,0.02,0.005],"InitiateMultipartUpload":[0.005,0.01,0.02,0.005],"CompleteMultipartUpload":[0.005,0.01,0.02,0.055]}},
      {"id":"eu-central-1","label":"Europe · Frankfurt","retrieval":[0,0.01,0.03,0.024],"storage":[0.0245,0.0135,0.005,0.0018],"write":[0.0054,0.01,0.02,0.06],"multipart":{"UploadPart":[0.0054,0.01,0.02,0.0054],"InitiateMultipartUpload":[0.0054,0.01,0.02,0.0054],"CompleteMultipartUpload":[0.0054,0.01,0.02,0.06]}},
      {"id":"ap-southeast-2","label":"Australia · Sydney","retrieval":[0,0.01,0.03,0.024],"storage":[0.025,0.0138,0.005,0.002],"write":[0.0055,0.01,0.02,0.06],"multipart":{"UploadPart":[0.0055,0.01,0.02,0.0055],"InitiateMultipartUpload":[0.0055,0.01,0.02,0.0055],"CompleteMultipartUpload":[0.0055,0.01,0.02,0.06]}}
    ],
    gcp: [
      {"id":"us-central1","label":"US · Iowa","storage":[0.02,0.01,0.004,0.0012],"write":[0.005,0.01,0.02,0.05],"retrieval":[0,0.01,0.02,0.05]},
      {"id":"us-west1","label":"US · Oregon","storage":[0.02,0.01,0.004,0.0012],"write":[0.005,0.01,0.02,0.05],"retrieval":[0,0.01,0.02,0.05]},
      {"id":"europe-west1","label":"Europe · Belgium","storage":[0.02,0.01,0.004,0.0012],"write":[0.005,0.01,0.02,0.05],"retrieval":[0,0.01,0.02,0.05]},
      {"id":"europe-west2","label":"UK · London","storage":[0.023,0.013,0.007,0.0025],"write":[0.005,0.01,0.02,0.05],"retrieval":[0,0.01,0.02,0.05]},
      {"id":"australia-southeast1","label":"Australia · Sydney","storage":[0.023,0.016,0.006,0.0025],"write":[0.005,0.01,0.02,0.05],"retrieval":[0,0.01,0.02,0.05]}
    ],
    azure: [
      {"id":"eastus","label":"US · East","retrieval":[0,0.01,0.03,0.02],"storage":[0.0208,0.0152,0.0036,0.00099],"write":[0.005,0.01,0.018,0.01]},
      {"id":"westus2","label":"US · West 2","retrieval":[0,0.01,0.03,0.02],"storage":[0.0184,0.01,0.0036,0.00099],"write":[0.005,0.01,0.018,0.01]},
      {"id":"westeurope","label":"Europe · Netherlands","retrieval":[0,0.01,0.03,0.024],"storage":[0.0196,0.01,0.0045,0.0018],"write":[0.0054,0.01,0.018,0.012]},
      {"id":"uksouth","label":"UK · South","retrieval":[0,0.011,0.03,0.0208],"storage":[0.0192,0.0105,0.0045,0.0018],"write":[0.0059,0.011,0.018,0.0106]},
      {"id":"australiaeast","label":"Australia · East","retrieval":[0,0.01,0.03,0.024],"storage":[0.02,0.011,0.0045,0.002],"write":[0.0055,0.01,0.018,0.012]}
    ]
  };
  // USD/GiB-month and USD/1,000 write requests, from official price catalogs.
  // Prices are snapshots, not live quotes. GCP hourly storage uses 730 hours/month.
  const tiers = [
    ["aws-standard", "aws", "Standard", 0, "Immediate", "STANDARD"],
    ["aws-ia", "aws", "Standard-IA", 30, "Immediate", "STANDARD_IA"],
    ["aws-instant", "aws", "Glacier Instant", 90, "Immediate", "GLACIER_IR"],
    ["aws-deep", "aws", "Deep Archive", 180, "Typically 12–48 hours", "DEEP_ARCHIVE"],
    ["gcp-standard", "gcp", "Standard", 0, "Immediate", "STANDARD"],
    ["gcp-nearline", "gcp", "Nearline", 30, "Immediate", "NEARLINE"],
    ["gcp-coldline", "gcp", "Coldline", 90, "Immediate", "COLDLINE"],
    ["gcp-archive", "gcp", "Archive", 365, "Immediate", "ARCHIVE"],
    ["azure-hot", "azure", "Hot", 0, "Immediate", "Hot"],
    ["azure-cool", "azure", "Cool", 30, "Immediate", "Cool"],
    ["azure-cold", "azure", "Cold", 90, "Immediate", "Cold"],
    ["azure-archive", "azure", "Archive", 180, "Can take up to 15 hours", "Archive"]
  ].map(([id, provider, name, minimumDays, latency, storageClass]) => ({id, provider, name, minimumDays, latency, storageClass}));
  const security = Object.freeze({credential_storage: 'pharos_database', encryption: 'provider_managed', restore_auth: 'cloud_account', interactive_upload: false});
  const compressionMeasurement = {"measured_at":"2026-10-23T15:09:47Z","method":"Full tar archive; zstd -3 -T2; SQLite online snapshot and APFS-cloned captures, preserved files, hosts and configuration. App and staging excluded.","uncompressed_bytes":142564121707,"compressed_bytes":41112195273,"compressed_percent":28.8376870567017,"file_count":42879};
  const defaults = {provider: 'gcp', region: 'us-central1', tier: 'gcp-standard', name: 'Cloud backup', bucket: 'pharos-backup',
    object: 'pharos/library.tar.zst', identity: 'pharos-backup-uploader', project: '', projectMode: 'existing', billingAccount: '', projectParent: '', profile: 'default', subscription: '',
    account: 'pharosbackup', group: 'pharos-backup', container: 'pharos', sizeGB: 140, compression: 30, cadence: 7,
    keep: 4, minimumAge: 1, retentionMode: 'count', retentionDays: 30, passwordProtected: false,
    time: '02:00', weekday: 0, timezone: 'America/Denver', egress: .12, tokenDays: 90, growthBasis: 'fallback', historyDays: 0};
  const text = (v, fallback) => typeof v === 'string' ? v : fallback;
  function bounded(v, fallback, min, max, integer = false) {
    const n = v === '' || v == null ? NaN : Number(v);
    const result = Number.isFinite(n) ? Math.max(min, Math.min(max, n)) : fallback;
    return integer ? Math.round(result) : result;
  }
  function normalize(input = {}) {
    const provider = Object.hasOwn(providers, input.provider) ? input.provider : tiers.find(t => t.id === input.tier)?.provider || defaults.provider;
    const tier = tiers.find(t => t.id === input.tier && t.provider === provider) || tiers.find(t => t.provider === provider);
    const region = regions[provider].find(r => r.id === input.region) || regions[provider][0];
    const p = {provider, tier: tier.id, region: region.id};
    for (const k of ['name','bucket','object','identity','project','billingAccount','projectParent','profile','subscription','account','group','container','time','timezone']) p[k] = text(input[k], defaults[k]);
    p.projectMode = input.projectMode === 'new' ? 'new' : 'existing';
    p.passwordProtected = input.passwordProtected === true || input.passwordProtected === 'true';
    for (const [k,min,max,integer] of [['sizeGB',.001,1000000],['compression',1,100],['keep',1,100,true],['minimumAge',1,3650,true],['retentionDays',1,3650,true],['egress',0,100],['weekday',0,6,true],['tokenDays',1,3650,true]]) p[k] = bounded(input[k], defaults[k], min, max, integer);
    p.cadence = [1,7,30].includes(Number(input.cadence)) ? Number(input.cadence) : defaults.cadence;
    p.retentionMode = provider === 'azure' || input.retentionMode === 'days' ? 'days' : 'count';
    p.growthBasis = ['measured_90_days','custom'].includes(input.growthBasis) ? input.growthBasis : 'fallback';
    p.growthGBPerMonth = p.growthBasis === 'fallback' ? p.sizeGB * .05 : bounded(input.growthGBPerMonth, p.sizeGB * .05, 0, 1e6);
    p.historyDays = bounded(input.historyDays, 0, 0, 90);
    return p;
  }
  const tierFor = p => tiers.find(t => t.id === p.tier);
  function rate(input, id) {
    const p = normalize(input), t = tiers.find(t => t.id === (id || p.tier));
    const region = regions[t.provider].find(r => r.id === p.region) || regions[t.provider][0];
    const i = tiers.filter(x => x.provider === t.provider).findIndex(x => x.id === t.id);
    return {...t, storage: region.storage[i], write: region.write[i], retrieval: region.retrieval[i], region, index: i};
  }
  function estimate(input, id) {
    const p = normalize(input), t = rate(p, typeof id === 'object' ? id.id : id), bytes = p.sizeGB * GB * p.compression / 100;
    // Current revision is retained indefinitely. Superseded versions also
    // respect the grace period. Ignore unbounded provider cleanup lag.
    const count = p.retentionMode === 'count' ? Math.max(p.keep, 1 + Math.ceil(p.minimumAge / p.cadence)) : Math.max(1,(p.provider === 'azure' ? 0 : 1) + Math.ceil(p.retentionDays / p.cadence));
    const billedCount = Math.max(count, t.minimumDays / p.cadence);
    const effectivePartMiB = Math.max(uploadPartMiB, Math.ceil(bytes / 10000 / 2 ** 20));
    const partBytes = effectivePartMiB * 2 ** 20, parts = Math.ceil(bytes / partBytes), runs = 30 / p.cadence;
    let perRun;
    if (t.provider === 'gcp') perRun = t.write / 1000; // One resumable object upload, independent of HTTP chunks.
    else if (t.provider === 'aws') {
      const m = t.region.multipart;
      perRun = bytes <= partBytes ? t.write / 1000 : (parts * m.UploadPart[t.index] + m.InitiateMultipartUpload[t.index] + m.CompleteMultipartUpload[t.index]) / 1000;
    } else perRun = (parts + 1) * t.write / 1000;
    const storageCost = bytes / GiB * count * t.storage;
    const earlyDeletion = bytes / GiB * (billedCount - count) * t.storage;
    const metadataCost = t.id === 'aws-deep' ? count * 8192 / GiB * t.region.storage[0] + billedCount * 32768 / GiB * t.storage : 0;
    const writesCost = perRun * runs;
    return {plan:p, tier:t, snapshotBytes:bytes, effectivePartMiB, count, billedCount, liveBytes:bytes * count, monthlyBytes:bytes * runs,
      monthlyWrites:(t.provider === 'gcp' || (t.provider === 'aws' && bytes <= partBytes) ? 1 : parts + (t.provider === 'aws' ? 2 : 1)) * runs,
      storageCost, earlyDeletion, metadataCost, writesCost, monthly:storageCost + earlyDeletion + metadataCost + writesCost,
      retrievalCost:bytes / GiB * t.retrieval, egressCost:bytes / GB * p.egress};
  }
  // Forecast actual full revisions over the next 365 days, starting empty.
  // Each revision keeps its upload-day size. Minimum-duration fees are charged
  // when a version expires, so the first-year estimate includes startup and
  // churn rather than multiplying a static monthly price by twelve.
  function calendarPeriods(startDate, days = 365) {
    const date=new Date(startDate);
    if(!Number.isFinite(date.getTime()))throw new RangeError('Invalid forecast date');
    const start=Date.UTC(date.getUTCFullYear(),date.getUTCMonth(),date.getUTCDate()),periods=[];
    const format=new Intl.DateTimeFormat('en-US',{month:'short',year:'numeric',timeZone:'UTC'});
    for(let day=0;day<days;day++){
      const current=new Date(start+day*864e5),key=current.toISOString().slice(0,7);
      let period=periods[periods.length-1];
      if(!period||period.key!==key){period={key,label:format.format(current),startDay:day,endDay:day};periods.push(period);}
      period.endDay=day+1;
    }
    return periods;
  }
  function forecast(input, id, startDate = new Date()) {
    const p = normalize(input), t = rate(p, id), daily = [], dailyBreakdown = [], storedBytes = [], versions = [];
    let uploadedBytes = 0, deletionCharges = 0, earliestPruneAgeDays = null;
    for (let day=0;day<365;day++) {
      // minimumStorageCost is early-deletion fees, not a minimum object size.
      let uploadCost = 0, minimumStorageCost = 0;
      if (day % p.cadence === 0) {
        if (versions.length) versions[versions.length-1].superseded = day;
        const e = estimate({...p,sizeGB:p.sizeGB+p.growthGBPerMonth*day/30}, t.id);
        versions.push({created:day,superseded:null,bytes:e.snapshotBytes});
        uploadedBytes += e.snapshotBytes;
        uploadCost = e.writesCost / (30/p.cadence);
      }
      for (let i=versions.length-1;i>=0;i--) {
        const v=versions[i], newer=versions.length-1-i;
        if(v.superseded==null) continue;
        const expired=p.retentionMode==='count' ? newer>=p.keep && day-v.superseded>=p.minimumAge : day-(p.provider==='azure'?v.created:v.superseded)>=p.retentionDays;
        if(!expired) continue;
        const age=day-v.created;
        earliestPruneAgeDays=earliestPruneAgeDays==null?age:Math.min(earliestPruneAgeDays,age);
        const remaining=Math.max(0,t.minimumDays-age);
        const fee=(v.bytes/GiB*t.storage+(t.id==='aws-deep'?32768/GiB*t.storage:0))*remaining/30;
        minimumStorageCost+=fee;deletionCharges+=fee;versions.splice(i,1);
      }
      const bytes=versions.reduce((n,v)=>n+v.bytes,0);
      let storageCost=bytes/GiB*t.storage/30;
      if(t.id==='aws-deep')storageCost+=versions.length*(8192/GiB*t.region.storage[0]+32768/GiB*t.storage)/30;
      dailyBreakdown.push({storageCost,uploadCost,minimumStorageCost});
      daily.push(storageCost+uploadCost+minimumStorageCost);
      storedBytes.push(bytes);
    }
    const sum=values=>values.reduce((a,b)=>a+b,0);
    const costs=(start,end)=>Object.fromEntries(['storageCost','uploadCost','minimumStorageCost'].map(key=>[key,sum(dailyBreakdown.slice(start,end).map(day=>day[key]))]));
    const months=calendarPeriods(startDate).map((period,i)=>{
      const start=period.startDay,end=period.endDay;
      return {...period,month:i+1,days:end-start,cost:sum(daily.slice(start,end)),...costs(start,end),storedGB:sum(storedBytes.slice(start,end))/(end-start)/GB};
    });
    // Always use next month's complete calendar period, even on the first of
    // this month. It includes growth and retained revisions since setup today.
    const firstMonthPeriod=months[1];
    return {firstMonth:firstMonthPeriod.cost,firstMonthLabel:firstMonthPeriod.label,year:sum(daily),month12:sum(daily.slice(-30)),
      libraryYearBytes:(p.sizeGB+p.growthGBPerMonth*365/30)*GB,uploadedBytes,deletionCharges,earliestPruneAgeDays,
      snapshotBytes:p.sizeGB*GB*p.compression/100, ...costs(0,365),daily,storedBytes,months};
  }
  function projection(input, startDate = new Date()) {
    const p=normalize(input);
    return {projected:forecast(p,undefined,startDate),flat:forecast({...p,growthBasis:'custom',growthGBPerMonth:0},undefined,startDate)};
  }
  function priceRange(input, id) {
    const p=normalize(input), forecasts=regions[p.provider].map(r=>forecast({...p,region:r.id},id));
    return Object.fromEntries(['firstMonth','year','month12'].map(key=>[key,{min:Math.min(...forecasts.map(e=>e[key])),max:Math.max(...forecasts.map(e=>e[key]))}]));
  }
  function validation(input, step = 2) {
    const p = normalize(input), errors = {};
    const fail = (key, ok, message) => { if (!ok) errors[key] = message; };
    fail('provider', input.provider == null || Object.hasOwn(providers,input.provider), 'Choose a supported provider.');
    fail('passwordProtected', input.passwordProtected == null || [true,false,'true','false'].includes(input.passwordProtected), 'Choose whether a recovery password is required.');
    fail('region', input.region == null || regions[p.provider].some(r=>r.id===input.region), 'Choose one of the priced locations for this provider.');
    fail('tier', p.tier !== 'azure-archive', 'Azure Archive cannot overwrite the current archive blob. Choose Hot, Cool or Cold for this single-file setup.');
    fail('name', p.name.trim().length > 0 && p.name.length <= 80 && !/[\x00-\x1f\x7f]/.test(p.name), 'Give this destination a name (up to 80 characters).');
    fail('object', /^[a-zA-Z0-9][a-zA-Z0-9_./-]{0,199}$/.test(p.object) && !p.object.split('/').includes('..') && !p.object.endsWith('/'), 'Use an archive path of letters, digits, dots, hyphens, underscores and slashes, without .. segments.');
    if (p.provider === 'aws' || p.provider === 'gcp') {
      fail('bucket', /^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$/.test(p.bucket) && !p.bucket.startsWith('xn--') && !p.bucket.endsWith('-s3alias') && !p.bucket.endsWith('--ol-s3') && !p.bucket.endsWith('--x-s3') && !p.bucket.endsWith('--table-s3'), 'Use an exact, globally unique bucket name: 3–63 lowercase letters, digits or hyphens.');
      if (p.provider === 'aws') {
        fail('identity', /^[a-zA-Z0-9_+=,.@-]{1,64}$/.test(p.identity), 'Use an IAM user name of 1–64 letters, digits or _+=,.@-.');
        fail('profile', /^[a-zA-Z0-9_.@-]{1,80}$/.test(p.profile), 'Enter the name of an existing AWS CLI profile.');
      } else {
        fail('projectMode', input.projectMode == null || ['existing','new'].includes(input.projectMode), 'Choose an existing project or create a new one.');
        fail('project', /^[a-z][a-z0-9-]{4,28}[a-z0-9]$/.test(p.project), `Enter ${p.projectMode==='new'?'a globally unique new':'an existing, billing-enabled'} GCP project ID (6–30 characters).`);
        if(p.projectMode==='new'){
          fail('billingAccount', /^[A-Za-z0-9]{6}-[A-Za-z0-9]{6}-[A-Za-z0-9]{6}$/.test(p.billingAccount), 'Enter an active billing account ID, such as 012ABC-345DEF-678ABC. Find it with gcloud billing accounts list.');
          fail('projectParent', p.projectParent==='' || /^(organizations|folders)\/[1-9][0-9]*$/.test(p.projectParent), 'Leave blank for a personal project, or use organizations/123456 or folders/123456.');
        }
        fail('identity', /^[a-z][a-z0-9-]{4,28}[a-z0-9]$/.test(p.identity), 'Use a service-account ID of 6–30 lowercase letters, digits or hyphens.');
        fail('bucket', !p.bucket.includes('goog') && !p.bucket.includes('google'), 'GCP bucket names cannot contain reserved Google names.');
      }
    } else {
      fail('account', /^[a-z0-9]{3,24}$/.test(p.account), 'Use a globally unique Azure storage-account name of 3–24 lowercase letters or digits.');
      fail('group', /^[a-zA-Z0-9_.()-]{1,90}$/.test(p.group) && !p.group.endsWith('.'), 'Use a resource-group name of up to 90 letters, digits, underscores, parentheses, hyphens or dots.');
      fail('container', /^[a-z0-9](?:[a-z0-9]|-(?!-)){1,61}[a-z0-9]$/.test(p.container), 'Use a container name of 3–63 lowercase letters, digits or single hyphens.');
      fail('subscription', /^[a-zA-Z0-9][a-zA-Z0-9_. -]{0,99}$/.test(p.subscription), 'Enter your Azure subscription ID or name.');
    }
    if (step >= 0) {
      fail('sizeGB', p.sizeGB * GB * p.compression / 100 <= 5 * 1024 * GiB, 'This prototype supports compressed archives up to 5 TiB.');
      fail('time', /^([01]\d|2[0-3]):[0-5]\d$/.test(p.time), 'Choose a valid upload start time.');
      try { new Intl.DateTimeFormat('en-US',{timeZone:p.timezone}).format(); } catch { errors.timezone = 'Enter a valid IANA time zone, such as America/Denver or Europe/London.'; }
      for (const [key,min,max,integer] of [['sizeGB',.001,1e6],['compression',1,100],['keep',1,100,true],['minimumAge',1,3650,true],['retentionDays',1,3650,true],['egress',0,100],['tokenDays',1,3650,true],['growthGBPerMonth',0,1e6]]) {
        if(key==='growthGBPerMonth'&&p.growthBasis!=='custom')continue;
        if ((['keep','minimumAge'].includes(key)&&p.retentionMode!=='count') || (key==='retentionDays'&&p.retentionMode!=='days') || (key==='tokenDays'&&p.provider!=='azure')) continue;
        if (input[key] != null) fail(key, input[key] !== '' && Number.isFinite(Number(input[key])) && Number(input[key]) >= min && Number(input[key]) <= max && (!integer || Number.isInteger(Number(input[key]))), `Enter ${integer ? 'a whole number' : 'a number'} from ${min} to ${max}.`);
      }
    }
    // Calculator requires no destination/account or schedule details.
    if(step===0)for(const key of Object.keys(errors))if(!['provider','region','tier','cadence','keep','retentionDays','minimumAge','sizeGB','compression','growthGBPerMonth'].includes(key))delete errors[key];
    return errors;
  }
  const q = value => "'" + String(value).replaceAll("'", "'\\''") + "'";
  function lifecycle(input) {
    const p = normalize(input);
    if (p.provider === 'aws') {
      const expiry = {NoncurrentDays:p.retentionMode === 'count' ? p.minimumAge : p.retentionDays};
      if (p.retentionMode === 'count' && p.keep > 1) expiry.NewerNoncurrentVersions = p.keep - 1;
      return {Rules:[{ID:'pharos-versions',Status:'Enabled',Filter:{Prefix:p.object},NoncurrentVersionExpiration:expiry,AbortIncompleteMultipartUpload:{DaysAfterInitiation:7}}]};
    }
    if (p.provider === 'gcp') {
      const condition = {isLive:false,daysSinceNoncurrentTime:p.retentionMode === 'count' ? p.minimumAge : p.retentionDays,matchesPrefix:[p.object]};
      if (p.retentionMode === 'count') condition.numNewerVersions = p.keep;
      return {rule:[{action:{type:'Delete'},condition}]};
    }
    return {rules:[{enabled:true,name:'pharos-versions',type:'Lifecycle',definition:{filters:{blobTypes:['blockBlob'],prefixMatch:[p.container+'/'+p.object]},actions:{version:{delete:{daysAfterCreationGreaterThan:p.retentionDays}}}}}]};
  }
  function destination(input) {
    const p = normalize(input), t = tierFor(p);
    return {...p, ...security, ...(p.passwordProtected?{encryption:'age_public_key',restore_auth:'cloud_account_and_password',encryption_setup:'pending',recovery_bundle_file:'recovery-bundle.json',include_recovery_bundle:true}:{}),format:p.passwordProtected?'full_archive_zstd_age_bundle_v1':'full_archive_zstd', storage_class:t.storageClass, bucket:p.provider === 'azure' ? p.account : p.bucket,
      versioning:true, test_object:p.object+'.roundtrip-test', upload_bandwidth_limit_mbps:null, effective_part_mib:Math.max(uploadPartMiB,Math.ceil(p.sizeGB*GB*p.compression/100/10000/2**20)), credential_file:'upload-credential.json', schedule_enabled:false, prototype:false, pricing_reviewed:reviewed};
  }
  // The helper is a local file, never a shell here-document while it prompts.
  // age reads the password from /dev/tty; private identities only cross pipes.
  const passwordHelper = String.raw`import hmac, json, os, shutil, subprocess, sys, tempfile
from pathlib import Path

root = Path(__file__).resolve().parent

def age(*args, data=None):
    return subprocess.run([age_bin, *args], input=data, stdout=subprocess.PIPE, check=True).stdout

def main():
    global age_bin
    os.umask(0o077)
    # Never fall back to stdin: queued shell commands are not a password source.
    tty = os.open('/dev/tty', os.O_RDWR)
    try:
        if not os.isatty(tty):
            raise RuntimeError('Open an interactive Terminal to set the recovery password.')
    finally:
        os.close(tty)
    age_bin = shutil.which('age')
    keygen = shutil.which('age-keygen')
    if not age_bin or not keygen:
        raise RuntimeError('Install age first (on macOS: brew install age), then retry this command.')
    draft_path = root / 'destination-draft.json'
    draft = json.loads(draft_path.read_text())
    if draft.get('passwordProtected') is not True or draft.get('encryption') != 'age_public_key':
        raise RuntimeError('This destination is not configured for password protection.')
    bundle_path = root / 'recovery-bundle.json'
    if bundle_path.exists() or draft.get('encryption_setup') == 'verified':
        raise RuntimeError('Recovery setup already exists. Keep it intact; do not regenerate its keys.')
    identity = subprocess.run([keygen], stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True).stdout
    try:
        recipient = subprocess.run([keygen, '-y'], input=identity, stdout=subprocess.PIPE, check=True).stdout.decode('ascii').strip()
        print('Choose a strong recovery password and confirm it. Nothing is sent to Pharos.', flush=True)
        sealed = age('--passphrase', '--armor', data=identity)
        print('Re-enter that password once to verify recovery before setup is saved.', flush=True)
        recovered = age('--decrypt', data=sealed)
        try:
            if not hmac.compare_digest(identity, recovered):
                raise RuntimeError('Recovery verification failed; no keys were saved.')
        finally:
            recovered = None
        bundle = {'version': 1, 'format': 'age-password-recovery-v1', 'recipient': recipient,
                  'encrypted_identity': sealed.decode('ascii')}
        # Only the password-encrypted identity and public recipient reach disk.
        with open(bundle_path, 'x', encoding='utf-8') as output:
            json.dump(bundle, output, indent=2)
        draft['public_recipient'] = recipient
        draft['encryption_setup'] = 'verified'
        fd, temporary = tempfile.mkstemp(prefix='.destination-', dir=root)
        try:
            with os.fdopen(fd, 'w', encoding='utf-8') as output:
                json.dump(draft, output, indent=2)
            os.replace(temporary, draft_path)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)
        print('Recovery verified. Only the public key and encrypted recovery bundle were saved.')
        print('Now copy and run the final block from Pharos. Uploads will not need your password.')
    finally:
        identity = None

if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.CalledProcessError) as error:
        print('Password setup did not finish: ' + str(error), file=sys.stderr)
        sys.exit(1)
`;
  function setup(input) {
    const stages=setupStages(input);
    if(stages.length!==1)throw new Error('Password-protected setup must be copied in separate stages. Use setupStages.');
    return stages[0].commands;
  }
  function setupStages(input) {
    const p = normalize(input), errors = validation(input);
    if (Object.keys(errors).length) throw new Error(Object.values(errors).join(' '));
    const t = tierFor(p), dir = p.provider === 'azure' ? p.account : p.bucket;
    let s = `#!/usr/bin/env bash
# Prepared for ${p.provider} / ${p.region}. Review before running; creates billable resources.
# Requires ${p.provider === 'aws' ? 'AWS CLI v2' : p.provider === 'gcp' ? 'gcloud CLI' : 'Azure CLI'}, Python 3 and a signed-in cloud administrator.
# Names are exactly those shown in Pharos. Availability is checked by the provider.
# Creates a NEW destination. Do not rerun the entire script after a partial failure.
set -euo pipefail
set +x
umask 077
PHAROS_SETUP="$HOME/Library/Application Support/Pharos/cloud-setup/${p.provider}/${dir}"
mkdir -p "$PHAROS_SETUP"
chmod 700 "$PHAROS_SETUP"
export PHAROS_SETUP
printf 'Private setup directory: %s\\n' "$PHAROS_SETUP"
# Keep settings and credentials from a prior attempt intact.
if [ -e "$PHAROS_SETUP/destination-draft.json" ] || [ -e "$PHAROS_SETUP/upload-credential.json" ]; then
  printf '%s\\n' 'Setup files already exist. Inspect the prior attempt before retrying.' >&2
  exit 1
fi
python3 - <<'PY'
import json, os
root = os.environ['PHAROS_SETUP']
config = json.loads(${JSON.stringify(JSON.stringify(destination(p)))})
policy = json.loads(${JSON.stringify(JSON.stringify(lifecycle(p)))})
json.dump(config, open(root+'/destination-draft.json','w'), indent=2)
json.dump(policy, open(root+'/lifecycle.json','w'), indent=2)
PY
`;
    const preparation=s;
    s='';
    if (p.provider === 'aws') s += `
export AWS_PROFILE=${q(p.profile)}
export AWS_DEFAULT_REGION=${q(p.region)}
export AWS_REGION=${q(p.region)}
aws sts get-caller-identity --query '{Account:Account,Arn:Arn}'
# Only provision a new bucket; us-east-1 creation can otherwise reuse an owned bucket.
if aws s3api head-bucket --bucket ${q(p.bucket)} 2>"$PHAROS_SETUP/bucket-check.err"; then
  printf '%s\\n' 'That bucket already exists. Choose an unused bucket name in Pharos.' >&2
  exit 1
fi
if ! grep -q '(404)' "$PHAROS_SETUP/bucket-check.err"; then
  cat "$PHAROS_SETUP/bucket-check.err" >&2
  printf '%s\\n' 'Could not verify that the bucket is new. Stop and inspect the error.' >&2
  exit 1
fi
aws s3api create-bucket --bucket ${q(p.bucket)} --region ${q(p.region)}${p.region === 'us-east-1' ? '' : ' --create-bucket-configuration '+q('LocationConstraint='+p.region)} >/dev/null
aws s3api put-public-access-block --bucket ${q(p.bucket)} --public-access-block-configuration 'BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true'
aws s3api put-bucket-ownership-controls --bucket ${q(p.bucket)} --ownership-controls 'Rules=[{ObjectOwnership=BucketOwnerEnforced}]'
aws s3api put-bucket-encryption --bucket ${q(p.bucket)} --server-side-encryption-configuration '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'
aws s3api put-bucket-versioning --bucket ${q(p.bucket)} --versioning-configuration Status=Enabled
aws s3api put-bucket-lifecycle-configuration --bucket ${q(p.bucket)} --lifecycle-configuration "file://$PHAROS_SETUP/lifecycle.json"
python3 - <<'PY'
import json, os
config = json.load(open(os.environ['PHAROS_SETUP']+'/destination-draft.json'))
policy = {'Version':'2012-10-17','Statement':[
 {'Effect':'Allow','Action':['s3:PutObject','s3:AbortMultipartUpload'],'Resource':['arn:aws:s3:::'+config['bucket']+'/'+config['object'],'arn:aws:s3:::'+config['bucket']+'/'+config['object']+'.roundtrip-test']},
 {'Effect':'Deny','Action':['s3:Get*','s3:List*','s3:Delete*'],'Resource':'*'}]}
json.dump(policy, open(os.environ['PHAROS_SETUP']+'/uploader-policy.json','w'))
PY
aws iam create-user --user-name ${q(p.identity)} >/dev/null
aws iam put-user-policy --user-name ${q(p.identity)} --policy-name PharosBackupUpload --policy-document "file://$PHAROS_SETUP/uploader-policy.json"
# Initial S3 versioning propagation can take 15 minutes. If import has no version ID, wait and retry.
# SDK must retain multipart upload IDs and ETags locally; it cannot list objects/parts.
aws iam create-access-key --user-name ${q(p.identity)} --query AccessKey --output json > "$PHAROS_SETUP/upload-credential.json"
`;
    if (p.provider === 'gcp') {
      const role = p.identity.replaceAll('-','_'), email = p.identity+'@'+p.project+'.iam.gserviceaccount.com';
      if(p.projectMode==='new'){
        const [parentKind,parentID]=p.projectParent.split('/');
        const parentFlag=parentID?` --${parentKind==='folders'?'folder':'organization'} ${q(parentID)}`:'';
        s += `
# Create exactly this project. A name collision, quota or permission error stops setup.
# Requires project-creation permission and permission to use the active billing account.
gcloud projects create ${q(p.project)}${parentFlag}
gcloud billing projects link ${q(p.project)} --billing-account ${q(p.billingAccount)}
`;
      }
      s += `
gcloud projects describe ${q(p.project)} --format='value(projectId)'
gcloud services enable storage.googleapis.com iam.googleapis.com --project ${q(p.project)}
gcloud storage buckets create ${q('gs://'+p.bucket)} --project ${q(p.project)} --location ${q(p.region)} --uniform-bucket-level-access --public-access-prevention --default-storage-class ${q(t.storageClass)}
# Provider-managed encryption is the default. No user encryption key is configured.
gcloud storage buckets update ${q('gs://'+p.bucket)} --versioning --clear-soft-delete --lifecycle-file "$PHAROS_SETUP/lifecycle.json"
gcloud iam service-accounts create ${q(p.identity)} --project ${q(p.project)} --display-name ${q(p.name)}
gcloud iam roles create ${q(role)} --project ${q(p.project)} --title 'Pharos backup upload and delete' --permissions storage.objects.create,storage.objects.delete --stage GA
gcloud storage buckets add-iam-policy-binding ${q('gs://'+p.bucket)} --member ${q('serviceAccount:'+email)} --role ${q('projects/'+p.project+'/roles/'+role)}
# Keys may be prohibited by organization policy. Never broaden the role to enable them.
gcloud iam service-accounts keys create "$PHAROS_SETUP/upload-credential.json" --iam-account ${q(email)} --project ${q(p.project)}
`;
    }
    if (p.provider === 'azure') s += `
az account show --subscription ${q(p.subscription)} --query '{Subscription:id,Tenant:tenantId}'
if [ "$(az storage account check-name --name ${q(p.account)} --subscription ${q(p.subscription)} --query nameAvailable -o tsv)" != 'true' ]; then
  printf '%s\\n' 'That storage account name is unavailable. Choose a new name in Pharos.' >&2
  exit 1
fi
az group create --name ${q(p.group)} --location ${q(p.region)} --subscription ${q(p.subscription)} >/dev/null
az storage account create --name ${q(p.account)} --resource-group ${q(p.group)} --location ${q(p.region)} --subscription ${q(p.subscription)} --sku Standard_LRS --kind StorageV2 --min-tls-version TLS1_2 --https-only true --allow-blob-public-access false >/dev/null
az storage account blob-service-properties update --account-name ${q(p.account)} --resource-group ${q(p.group)} --subscription ${q(p.subscription)} --enable-versioning true --enable-delete-retention false --enable-container-delete-retention false >/dev/null
export AZURE_STORAGE_ACCOUNT=${q(p.account)}
AZURE_STORAGE_KEY="$(az storage account keys list --resource-group ${q(p.group)} --account-name ${q(p.account)} --subscription ${q(p.subscription)} --query '[0].value' -o tsv)"
export AZURE_STORAGE_KEY
az storage container create --name ${q(p.container)} --public-access off --auth-mode key >/dev/null
PHAROS_EXPIRY="$(python3 -c 'from datetime import datetime,timedelta,timezone; print((datetime.now(timezone.utc)+timedelta(days=${p.tokenDays})).strftime("%Y-%m-%dT%H:%MZ"))')"
export PHAROS_EXPIRY
az storage container policy create --container-name ${q(p.container)} --name pharos-uploader --permissions cw --expiry "$PHAROS_EXPIRY" --auth-mode key >/dev/null
az storage container generate-sas --name ${q(p.container)} --policy-name pharos-uploader --https-only --auth-mode key -o tsv > "$PHAROS_SETUP/upload.sas"
unset AZURE_STORAGE_KEY AZURE_STORAGE_ACCOUNT
python3 - <<'PY'
import json, os
root = os.environ['PHAROS_SETUP']
json.dump({'sas':open(root+'/upload.sas').read().strip(),'expires_at':os.environ.get('PHAROS_EXPIRY')},open(root+'/upload-credential.json','w'))
os.remove(root+'/upload.sas')
PY
az storage account management-policy create --account-name ${q(p.account)} --resource-group ${q(p.group)} --subscription ${q(p.subscription)} --policy "@$PHAROS_SETUP/lifecycle.json" >/dev/null
# Grant read access to the administrator's CLI login, never the Pharos SAS.
# A management-plane Owner role does not itself grant blob data access.
PHAROS_RECOVERY_OID="$(az account get-access-token --subscription ${q(p.subscription)} --resource https://storage.azure.com/ --query accessToken -o tsv | python3 -c 'import base64,json,uuid,sys; token=sys.stdin.read().strip().split("."); claims=json.loads(base64.urlsafe_b64decode(token[1]+"="*(-len(token[1])%4))); print(str(uuid.UUID(claims["oid"])))')"
PHAROS_RECOVERY_KIND="$(az account show --subscription ${q(p.subscription)} --query user.type -o tsv)"
case "$PHAROS_RECOVERY_KIND" in
  user) PHAROS_RECOVERY_KIND=User ;;
  servicePrincipal) PHAROS_RECOVERY_KIND=ServicePrincipal ;;
  *) printf '%s\\n' 'Unsupported recovery login type; assign Blob Data Reader to your recovery login explicitly.' >&2; exit 1 ;;
esac
PHAROS_STORAGE_SCOPE="$(az storage account show --name ${q(p.account)} --resource-group ${q(p.group)} --subscription ${q(p.subscription)} --query id -o tsv)"
az role assignment create --subscription ${q(p.subscription)} --assignee-object-id "$PHAROS_RECOVERY_OID" --assignee-principal-type "$PHAROS_RECOVERY_KIND" --role 'Storage Blob Data Reader' --scope "$PHAROS_STORAGE_SCOPE/blobServices/default/containers/${p.container}" >/dev/null
unset PHAROS_RECOVERY_OID PHAROS_RECOVERY_KIND PHAROS_STORAGE_SCOPE
# Azure data-role assignments can take several minutes to propagate; retry a download after that delay.

`;
    s += `
chmod 600 "$PHAROS_SETUP/upload-credential.json" "$PHAROS_SETUP/destination-draft.json"
printf 'Setup files: %s\\n' "$PHAROS_SETUP"
# Save destination-draft.json and upload-credential.json for import into Pharos.
# Return to Test in Pharos to import this folder and verify the upload/download round trip.
# After verifying a full restore, enable the schedule in Pharos. Setup alone never starts automatic uploads.
# The working uploader stores the credential directly in the Pharos database.
# It uses the storage class in the draft for every new archive revision.
# ${p.passwordProtected?'The uploader encrypts to public_recipient; it must embed recovery-bundle.json outside the encrypted payload in EVERY completed backup. Restore requires the downloaded bundle and recovery password.':'No upload password or separate recovery encryption key. Restore through your cloud account.'}
`;
    const wrap=body=>"bash <<'PHAROS_CLOUD_SETUP'\n"+body+'\nPHAROS_CLOUD_SETUP\n';
    if(!p.passwordProtected)return [{id:'setup',commands:wrap(preparation+s)}];
    const before=preparation.replace('set +x\n',`set +x
if ! command -v age >/dev/null || ! command -v age-keygen >/dev/null; then
  printf '%s\\n' 'Install age first (on macOS: brew install age), then copy this block again.' >&2
  exit 1
fi
`)+`\ncat > "$PHAROS_SETUP/setup-password.py" <<'PHAROS_PASSWORD_HELPER'\n${passwordHelper}PHAROS_PASSWORD_HELPER\nchmod 600 "$PHAROS_SETUP/setup-password.py"\nprintf '%s\\n' 'Preparation complete. Run the separate password command next. No cloud resources have been created.'\n`;
    const after=`set -euo pipefail
set +x
umask 077
PHAROS_SETUP="$HOME/Library/Application Support/Pharos/cloud-setup/${p.provider}/${dir}"
export PHAROS_SETUP
if [ -e "$PHAROS_SETUP/upload-credential.json" ]; then
  printf '%s\\n' 'Upload credentials already exist. Inspect the prior attempt before retrying.' >&2
  exit 1
fi
python3 - <<'PY'
import json, os
root = os.environ['PHAROS_SETUP']
draft = json.load(open(root+'/destination-draft.json'))
bundle = json.load(open(root+'/recovery-bundle.json'))
expected = json.loads(${JSON.stringify(JSON.stringify(destination(p)))})
# Refuse a skipped password step or stale commands for another configuration.
if any(draft.get(key) != value for key, value in expected.items() if key != 'encryption_setup'):
    raise SystemExit('Settings changed. Use the final commands for the prepared destination.')
if draft.get('encryption_setup') != 'verified' or bundle.get('version') != 1 or bundle.get('format') != 'age-password-recovery-v1' or not bundle.get('recipient', '').startswith('age1') or draft.get('public_recipient') != bundle.get('recipient') or not bundle.get('encrypted_identity', '').startswith('-----BEGIN AGE ENCRYPTED FILE-----'):
    raise SystemExit('Complete and verify the separate password setup command first.')
PY
`+s;
    return [{id:'before-password',commands:wrap(before)},{id:'password',commands:`python3 "$HOME/Library/Application Support/Pharos/cloud-setup/${p.provider}/${dir}/setup-password.py" </dev/tty\n`},{id:'after-password',commands:wrap(after)}];
  }
  function prompt(input) {
    const p = normalize(input), t = tierFor(p);
    const projectPlan = p.provider === 'gcp' ? (p.projectMode === 'new' ? `Create project ${p.project} under ${p.projectParent || 'no parent'}, link billing account ${p.billingAccount}, then enable Storage and IAM APIs before creating the bucket. Stop on availability, quota or permission errors. ` : 'Use the existing project without changing its billing account. ') : '';
    return `${projectPlan}Use the available connection speed without a Pharos upload speed limit. Review this fully specified Pharos cloud backup setup and return ${p.passwordProtected?'two independently copyable Bash blocks, separated by one standalone Terminal command for password setup. Never combine all three or put an interactive password prompt inside a pasted here-document':'one Bash script'}. Do not change these values or add random suffixes. Explain any provider limitation or unavailable name before changing the configuration.\n\n${JSON.stringify(destination(p),null,2)}\n\nUse ${providers[p.provider].name} in ${p.region}, ${t.name}. ${providers[p.provider].permissions} Enable native object versioning. ${p.retentionMode === 'count' ? `Keep ${p.keep} completed revisions total, including the current revision, and retain superseded revisions for at least ${p.minimumAge} days.` : `Expire superseded revisions after ${p.retentionDays} days; leave the current revision available.`} Cleanup is asynchronous. One full compressed library archive is stored under the configured object name; upload it in resumable parts. Reserve the exact additional object name ${p.object}.roundtrip-test for small round-trip tests, with the same restricted write permissions and no read/list access. Store the upload credential value directly in the Pharos database for unattended backups. Use provider-managed encryption and HTTPS. ${p.passwordProtected?'Generate an age key pair locally. Use age passphrase encryption to wrap the private identity, with password entry and confirmation only in the controlling terminal. Verify the encrypted identity by asking for the password once more. Never persist plaintext private keys or passwords. Save only the public recipient and encrypted recovery-bundle.json. The final Bash block must refuse to provision unless this setup is verified. Each future backup must embed that bundle outside its encrypted payload, so restore needs only the downloaded backup and password, with no separate key file. The uploader embeds this recovery bundle and the restore verifier asks for the password only in Terminal.':'Restore through the cloud account with read access, without an archive password or a separate recovery key.'} Never grant read/list access to the upload identity.\n\nStage credentials only in private files, never print or paste them into chat. Keep administrator credentials separate. Verify generated lifecycle JSON, scoped permissions and exact region/name values against the official provider documentation. These commands provision cloud resources. Return to Pharos Test to import the setup folder and run real uploads and restore validation. Enable scheduling in Pharos only after a verified full restore. Do not execute the script until I review it.`;
  }
  function restoreDownload(input, receipt, directory) {
    const p=normalize(input);
    const errors=validation(input);if(Object.keys(errors).length)throw new Error(Object.values(errors).join(' '));
    if(!receipt?.version||!receipt?.object)throw new Error('An upload receipt with an exact object version is required.');
    if(!['sample','full'].includes(receipt.kind)||receipt.object!==p.object+(receipt.kind==='sample'?'.roundtrip-test':''))throw new Error('The upload receipt belongs to a different archive.');
    if(typeof receipt.version!=='string'||receipt.version.length>4096||/[\x00-\x1f\x7f]/.test(receipt.version))throw new Error('The recorded object version is invalid.');
    if(typeof directory!=='string'||!directory.trim()||/[\x00-\x1f\x7f]/.test(directory))throw new Error('Choose a local folder for the downloaded archive.');
    if(!directory.trim().startsWith('/')&&!directory.trim().startsWith('~/'))throw new Error('Use an absolute folder path or a path starting with ~/.');
    const file=receipt.kind==='sample'?'round-trip-test.pharos':p.passwordProtected?'library.pharos':'library.tar.zst';
    const folder=directory.trim(),path=folder.startsWith('~/')?'"$HOME"/'+q(folder.slice(2)):q(folder);
    let command;
    if(p.provider==='gcp')command=`gcloud storage cp ${q('gs://'+p.bucket+'/'+receipt.object+'#'+receipt.version)} "$PHAROS_RESTORE_DIR/${file}.partial" --project ${q(p.project)}`;
    else if(p.provider==='aws')command=`aws s3api get-object --profile ${q(p.profile)} --region ${q(p.region)} --bucket ${q(p.bucket)} --key ${q(receipt.object)} --version-id ${q(receipt.version)} "$PHAROS_RESTORE_DIR/${file}.partial"`;
    else command=`az storage blob download --subscription ${q(p.subscription)} --auth-mode login --account-name ${q(p.account)} --container-name ${q(p.container)} --name ${q(receipt.object)} --version-id ${q(receipt.version)} --file "$PHAROS_RESTORE_DIR/${file}.partial"`;
    if(p.provider==='aws'&&p.tier==='aws-deep'){
      const head=`aws s3api head-object --profile ${q(p.profile)} --region ${q(p.region)} --bucket ${q(p.bucket)} --key ${q(receipt.object)} --version-id ${q(receipt.version)} --query Restore --output text`;
      command=`# Deep Archive retrieval can take 12–48 hours and incurs retrieval charges.\nPHAROS_RESTORE_STATUS="$(${head})"\ncase "$PHAROS_RESTORE_STATUS" in\n  *'ongoing-request="false"'*|*'ongoing-request="true"'*) ;;\n  *) aws s3api restore-object --profile ${q(p.profile)} --region ${q(p.region)} --bucket ${q(p.bucket)} --key ${q(receipt.object)} --version-id ${q(receipt.version)} --restore-request '${JSON.stringify({Days:1,GlacierJobParameters:{Tier:'Standard'}})}' ;;\nesac\nuntil [[ "$PHAROS_RESTORE_STATUS" == *'ongoing-request="false"'* ]]; do\n  printf '%s\\n' 'Waiting for AWS to make this revision available… (Ctrl-C stops waiting)'\n  sleep 60\n  PHAROS_RESTORE_STATUS="$(${head})"\ndone\n`+command;
    }
    return `bash <<'PHAROS_DOWNLOAD'\n${receipt.preview?'# Prototype example only: no object has been uploaded.\n# Version below is illustrative. This block stops before any download.\nprintf \'%s\\n\' \'Preview only: no cloud download is available yet.\' >&2\nexit 1\n\n':''}# Use your cloud account with download access, separate from the Pharos uploader.\nset -euo pipefail\numask 077\nPHAROS_RESTORE_DIR=${path}\nmkdir -p "$PHAROS_RESTORE_DIR"\nif ! mkdir "$PHAROS_RESTORE_DIR/.pharos-download.lock" 2>/dev/null; then\n  printf '%s\\n' 'A download is running or was interrupted here. Choose a new empty folder.' >&2\n  exit 1\nfi\ntrap 'rmdir -- "$PHAROS_RESTORE_DIR/.pharos-download.lock"' EXIT\nif [ -e "$PHAROS_RESTORE_DIR/${file}" ] || [ -e "$PHAROS_RESTORE_DIR/${file}.partial" ]; then\n  printf '%s\\n' 'Download file already exists. Choose an empty folder in Pharos.' >&2\n  exit 1\nfi\ntrap 'rm -f -- "$PHAROS_RESTORE_DIR/${file}.partial"; rmdir -- "$PHAROS_RESTORE_DIR/.pharos-download.lock"' EXIT\n${command}\nmv -n "$PHAROS_RESTORE_DIR/${file}.partial" "$PHAROS_RESTORE_DIR/${file}"\nif [ -e "$PHAROS_RESTORE_DIR/${file}.partial" ]; then\n  printf '%s\\n' 'Destination appeared during download; it was not overwritten.' >&2\n  exit 1\nfi\nrmdir -- "$PHAROS_RESTORE_DIR/.pharos-download.lock"\ntrap - EXIT\nPHAROS_DOWNLOAD\n`;
  }
  window.pharosCloudBackup = {defaults,security,providers,regions,tiers,reviewed,compressionMeasurement,normalize,rate,estimate,calendarPeriods,forecast,projection,priceRange,validation,lifecycle,destination,setup,setupStages,prompt,formatCost,restoreDownload};

  const mount = document.getElementById('cloudBackupPlanner');
  if (!mount) return;
  const prefKey = 'pharos-cloud-backup-configurations-v2';
  const node = (tag, text, cls) => { const n = document.createElement(tag); if (text != null) n.textContent = text; if (cls) n.className = cls; return n; };
  const money = value => new Intl.NumberFormat('en-US',{style:'currency',currency:'USD',maximumFractionDigits:2}).format(value);
  const number = value => new Intl.NumberFormat('en-US',{maximumFractionDigits:1}).format(value);
  const size = value => `${number(value / GB)} GB`;
  function newDraft(){
    const suffix = Math.random().toString(36).slice(2,8);
    const draft = {...defaults,bucket:'pharos-backup-'+suffix,account:'pharos'+suffix,identity:'pharos-backup-'+suffix,group:'pharos-backup-'+suffix};
    try { draft.timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || defaults.timezone; } catch {}
    return draft;
  }
  let raw = newDraft();
  let saved = window.pharosPrefs?.get(prefKey,[]) || [];
  saved = Array.isArray(saved) ? saved.slice(0,20).filter(p => p && typeof p === 'object').map(p => ({...normalize(p),id:text(p.id,'')})) : [];
  let step = 0, submitted = false, editedSize = false, measured = false, editing = '', saving = false, providerShown = '', tierProvider = '', tierRows = [];
  const fields = {}, allInputs = {}, touched = new Set();
  let latestGrowth, latestLibraryBytes, chartWidth=0, selectedMonth=0;
  const emptyWorkflow=()=>({setup:false,sample:null,full:null,sampleVerified:false,restoreVerified:false,sampleError:'',fullError:'',restoreError:''});
  let workflow=emptyWorkflow(),workflowPlan='',workflowNotice='';
  const button = (label, click, cls = '') => { const b = node('button',label,'pharos-button '+cls); b.type = 'button'; b.onclick = click; return b; };
  const sheet = node('style',`
    .cloud-wizard{margin:24px 0;min-width:0}.cloud-wizard h3{font:700 23px var(--serif,serif);margin:0 0 8px}.cloud-wizard h4{margin:0 0 12px;font-size:15px}.cloud-intro{max-width:760px}
    .cloud-card{border:1px solid var(--line);border-radius:12px;background:var(--panel);overflow:hidden;min-width:0}.cloud-card-header{display:flex;align-items:center;border-bottom:1px solid var(--line);padding:0 12px;gap:10px}.cloud-header-actions{display:flex;gap:6px;flex-shrink:0}.cloud-header-actions button{font-size:12px;padding:6px 9px}
    .cloud-steps{display:grid;grid-template-columns:repeat(6,minmax(0,1fr));flex:1;min-width:0;margin:0;gap:0}.cloud-steps button{display:flex;align-items:center;justify-content:center;gap:7px;border:0;border-bottom:2px solid transparent;border-radius:0;background:none;padding:14px 8px;font-size:12px;color:var(--muted)}.cloud-steps [aria-current=step]{border-bottom-color:var(--accent);color:var(--ink)}.cloud-steps span{display:inline-grid;place-content:center;width:18px;height:18px;border-radius:50%;border:1px solid currentColor;font-size:10px}.cloud-steps [aria-current=step] span{color:var(--accent)}
    .cloud-layout{display:flex;gap:24px;align-items:flex-start;padding:20px}.cloud-main{flex:1.2 1 0}.cloud-aside{flex:.8 1 0;padding-left:20px;border-left:1px solid var(--line)}.cloud-layout-wide .cloud-main{flex:1 1 100%}.cloud-main,.cloud-aside,.cloud-step{min-width:0}
    .cloud-fields{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,180px),1fr));gap:14px;margin-top:14px}.cloud-fields.cloud-calculator-fields{grid-template-columns:repeat(2,minmax(0,1fr));margin-top:0}.cloud-field{display:grid;gap:5px;align-content:start;min-width:0;font-size:12px}.cloud-field input,.cloud-field select{box-sizing:border-box;width:100%;min-width:0;background:var(--bg);color:var(--ink);border:1px solid var(--line);border-radius:6px;padding:8px;font:inherit}.cloud-field small{color:var(--muted);line-height:1.45}.cloud-field .cloud-error{color:var(--bad)}.cloud-field [aria-invalid=true]{border-color:var(--bad)}
    .cloud-wizard [hidden]{display:none}.cloud-wizard details{margin-top:14px}.cloud-wizard summary{cursor:pointer;padding:7px 0;font-size:12px}.cloud-wizard details>.cloud-fields{margin-top:10px}.cloud-note{color:var(--muted);font-size:11px;line-height:1.55;overflow-wrap:anywhere}.cloud-limit{padding:8px 10px;border-left:2px solid var(--accent);background:var(--bg);margin:10px 0}#cloud-detailed-options{border-top:1px solid var(--line);padding-top:6px;margin-top:14px}#cloud-detailed-options>summary{font-weight:600}#cloud-detailed-options>h4{font-size:13px;margin-top:18px}
    .cloud-retention-warning{font-size:12px;line-height:1.5;color:var(--ink)}.cloud-retention-warning strong{display:block;color:var(--accent);margin-bottom:4px}
    .cloud-footer{display:flex;flex-wrap:wrap;justify-content:space-between;align-items:center;gap:8px 16px;border-top:1px solid var(--line);padding:12px 20px}.cloud-actions{display:flex;flex-wrap:wrap;align-items:center;gap:8px}.cloud-actions .primary{margin-left:auto}.cloud-footer>.cloud-actions{margin-left:auto}.cloud-status{font-size:12px;color:var(--sea);margin:0}.cloud-status.error{color:var(--bad)}
    .cloud-results{width:100%;min-width:0}.cloud-totals{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:16px;padding-bottom:12px;border-bottom:1px solid var(--line)}.cloud-totals span{display:block;color:var(--muted);font-size:11px;margin-bottom:5px}.cloud-totals strong{font-size:23px;line-height:1.15;display:block;font-variant-numeric:tabular-nums;color:var(--ink);overflow-wrap:anywhere}
    .cloud-chart-legend{display:flex;flex-wrap:wrap;gap:6px 14px;font-size:12px;color:var(--muted);margin:12px 0 5px}.cloud-chart-legend span{display:flex;gap:6px;align-items:center}.cloud-chart-legend i{display:block;box-sizing:border-box;width:10px;height:10px;flex-shrink:0}.cloud-chart-scenarios{margin-top:6px}.cloud-legend-flat{border:1px solid var(--muted);opacity:.55}.cloud-legend-projected{border:1px dotted var(--ink)}.cloud-legend-storage{background:var(--sea)}.cloud-legend-upload{background:var(--accent)}.cloud-legend-minimum{background:var(--bad)}
    .cloud-chart{margin:0;position:relative;min-width:0}.cloud-chart figcaption{font-size:13px;font-weight:550;margin:8px 0 3px}.cloud-chart svg{display:block;width:100%;height:auto;overflow:visible;border-radius:4px}.cloud-chart svg:focus-visible{outline:2px solid var(--accent);outline-offset:3px}.cloud-chart-tick{font:12px var(--sans,sans-serif);fill:var(--muted)}.cloud-chart-grid{stroke:var(--line);stroke-width:.7}.cloud-bar-storage{fill:var(--sea)}.cloud-bar-upload{fill:var(--accent)}.cloud-bar-minimum{fill:var(--bad)}.cloud-bar-flat{opacity:.5}.cloud-bar-total{fill:none;stroke:var(--ink);stroke-width:.6}.cloud-bar-projected .cloud-bar-total{stroke-dasharray:1 2}.cloud-chart-cursor{fill:var(--ink);opacity:.08}.cloud-chart-readout{display:grid;gap:5px;margin-top:8px;font-size:12px;color:var(--muted);line-height:1.45;overflow-wrap:anywhere}.cloud-chart-readout>strong{color:var(--ink);font-weight:550}.cloud-cost-row span{display:block}.cloud-cost-row b{font-weight:550;color:var(--ink)}.cloud-results>.cloud-note{margin:8px 0 0;font-size:11px}
    .cloud-class-choices{display:grid;gap:10px;margin:12px 0}.cloud-class-choice{display:grid;grid-template-columns:auto 1fr;gap:5px 8px;align-items:center;font-size:12px}.cloud-class-choice small{grid-column:2;color:var(--muted);font-size:11px}.cloud-facts{margin:14px 0;display:grid;gap:8px}.cloud-facts div{padding-bottom:7px;border-bottom:1px solid var(--line)}.cloud-facts dt{color:var(--muted);font-size:11px}.cloud-facts dd{margin:4px 0 0;font-size:12px;overflow-wrap:anywhere}.cloud-wizard pre{margin:8px 0 0;padding:12px;border-radius:7px;max-height:400px;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;background:var(--bg);font:11px/1.5 ui-monospace,SFMono-Regular,monospace}.cloud-review~.cloud-actions{margin-top:14px}
    .cloud-beta{color:var(--muted);font-size:12px;line-height:1.3}.cloud-ongoing{margin-top:20px;min-width:0}.cloud-ongoing article{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:16px;margin:12px 0;min-width:0}.cloud-ongoing h4{margin:0 0 8px}.cloud-schedule-settings{margin-top:12px}.cloud-schedule-settings .cloud-fields{margin:12px 0;grid-template-columns:repeat(auto-fit,minmax(min(200px,100%),1fr))}.cloud-history-select{display:block;max-width:100%;margin:12px 0}.cloud-saved{border-top:1px solid var(--line);padding:18px 20px}.cloud-saved-item{padding:10px 0;border-bottom:1px solid var(--line)}.cloud-saved-item strong{display:block;overflow-wrap:anywhere}.cloud-saved-item p{margin:5px 0}.cloud-saved-item .cloud-actions{margin-top:7px}
    .cloud-fields.cloud-protection-fields{grid-template-columns:1fr;max-width:520px;margin:0}.cloud-command-stage{border-top:1px solid var(--line);padding:18px 0}.cloud-command-stage h4{margin-bottom:8px}.cloud-command-stage .cloud-note{max-width:760px}
    .cloud-run-layout{margin-top:20px;display:grid;grid-template-columns:minmax(0,1.3fr) minmax(230px,.7fr);gap:24px;align-items:start}.cloud-run-block{border-top:1px solid var(--line);padding:18px 0 0;margin-top:18px}.cloud-run-block h4{margin-bottom:6px}.cloud-run-block>.cloud-field{margin:12px 0}.cloud-run-layout .cloud-fields{grid-template-columns:1fr}.cloud-run-layout pre{max-height:220px}.cloud-preview-banner{padding:10px 12px;border:1px dashed var(--line);border-radius:6px;font-size:12px;color:var(--muted);margin:12px 0 18px}.cloud-run-result{padding:12px;border:1px solid var(--line);border-radius:6px;font-size:13px;margin-top:12px}.cloud-run-result.success{border-color:var(--sea)}.cloud-run-result.error{border-color:var(--bad)}.cloud-run-result strong{display:block;margin-bottom:5px}.cloud-run-checks{list-style:none;padding:0;margin:12px 0;display:grid;gap:9px;font-size:12px}.cloud-run-checks li{padding-left:18px;position:relative}.cloud-run-checks li::before{content:'○';position:absolute;left:0;color:var(--muted)}.cloud-run-checks li.complete::before{content:'✓';color:var(--sea)}.cloud-run-metadata{background:var(--bg);padding:16px;border-radius:8px;min-width:0}.cloud-run-metadata h4{margin-bottom:8px}.cloud-preview-scenarios{max-width:500px}.cloud-preview-scenarios select{font:inherit}.cloud-setup-check{display:flex;align-items:flex-start;gap:8px;font-size:13px;margin:16px 0}.cloud-setup-check input{margin-top:4px;flex-shrink:0}
    .cloud-cost-tooltip[hidden]{display:none}.cloud-cost-tooltip{box-sizing:border-box;min-width:min(220px,calc(100vw - 16px))}.cloud-cost-tooltip .chart-tooltip-row{align-items:center}.cloud-cost-tooltip-value{margin-left:auto;padding-left:16px;white-space:nowrap}.cloud-cost-tooltip-total{border-top:1px solid var(--line);padding-top:5px;margin-top:2px;font-weight:600}.cloud-cost-tooltip-context{color:var(--muted)}
    @media(max-width:1100px){.cloud-steps{grid-template-columns:repeat(3,minmax(0,1fr))}.cloud-steps button{padding:10px 6px}}
    @media(max-width:850px){.cloud-layout{flex-direction:column;gap:16px;padding:16px}.cloud-main,.cloud-aside{flex:0 1 auto;width:100%}.cloud-aside{border-left:0;border-top:1px solid var(--line);padding:16px 0 0}.cloud-footer{padding:12px 16px}.cloud-run-layout{grid-template-columns:1fr}}
    @media(max-width:440px){.cloud-fields.cloud-calculator-fields{grid-template-columns:1fr}.cloud-card-header{padding:0 8px;gap:0;flex-wrap:wrap}.cloud-steps{flex-basis:100%}.cloud-steps button{padding:11px 3px;gap:4px;font-size:12px}.cloud-steps span{width:15px;height:15px;font-size:10px}.cloud-header-actions{margin-left:auto;padding:6px 0}.cloud-layout{padding:12px}.cloud-saved{padding:12px}.cloud-totals strong{font-size:21px}}
  `);
  document.head.append(sheet);
  // Share the other charts' tooltip styling and portal outside clipped cards.
  const chartTooltip=node('div',null,'chart-tooltip cloud-cost-tooltip');chartTooltip.id='cloudBackupCostTooltip';chartTooltip.setAttribute('role','tooltip');chartTooltip.hidden=true;document.body.append(chartTooltip);
  function hideChartTooltip(){chartTooltip.hidden=true;}
  window.addEventListener('scroll',hideChartTooltip,true);window.addEventListener('resize',hideChartTooltip);window.addEventListener('blur',hideChartTooltip);
  window.addEventListener('keydown',event=>{if(event.key==='Escape')hideChartTooltip();});
  document.addEventListener('pointerdown',event=>{if(!event.target.closest('.cloud-chart'))hideChartTooltip();});
  mount.className = 'cloud-wizard';
  const invitation=node('div'),card=node('section',null,'cloud-card');card.id='cloudBackupWizardCard';card.hidden=true;
  const explore=button('Explore cloud backup options',()=>{invitation.hidden=true;card.hidden=false;explore.setAttribute('aria-expanded','true');refresh();void loadCloud();panels[step].focus({preventScroll:true});card.scrollIntoView({block:'nearest'});});
  explore.id='cloud-explore';explore.setAttribute('aria-controls',card.id);explore.setAttribute('aria-expanded','false');
  const exploreRow=node('div',null,'cloud-actions'),beta=node('span','Beta','chip cloud-beta');beta.title='Cloud backups are in beta; verify a restore before relying on them.';exploreRow.append(explore,beta);invitation.append(exploreRow,node('p','Compare backup options in your own cloud account.','cloud-note'));const ongoingPanel=node('section',null,'cloud-ongoing');ongoingPanel.hidden=true;mount.append(invitation,card,ongoingPanel);
  const header=node('div',null,'cloud-card-header'),nav=node('nav',null,'cloud-steps');nav.setAttribute('aria-label','Cloud backup setup steps');
  const stepNames=['Calculator','Configure','Commands','Test','First backup','Test restore'];
  const navButtons=stepNames.map((label,i)=>{const b=button('',()=>go(i));b.setAttribute('aria-label',`Step ${i+1}: ${label}`);b.append(node('span',i+1),document.createTextNode(label));nav.append(b);return b;});
  const reset=button('Reset',()=>resetDraft());reset.setAttribute('aria-label','Reset cloud backup options');
  const close=button('Close',()=>{hideChartTooltip();card.hidden=true;invitation.hidden=false;explore.setAttribute('aria-expanded','false');explore.focus();renderOngoing();});close.setAttribute('aria-label','Close cloud backup options');
  const headerActions=node('div',null,'cloud-header-actions');headerActions.append(reset,close);header.append(nav,headerActions);
  const layout=node('div',null,'cloud-layout'),main=node('div',null,'cloud-main'),aside=node('aside',null,'cloud-aside');aside.setAttribute('aria-label','Cloud backup cost forecast');
  const panels=stepNames.map((_,i)=>{const section=node('section',null,'cloud-step');section.id='cloud-step-'+i;section.tabIndex=-1;main.append(section);return section;});
  const status=node('p','','cloud-status');status.setAttribute('role','status');status.setAttribute('aria-live','polite');layout.append(main,aside);card.append(header,layout);
  function message(text,error=false){status.textContent=text;status.classList.toggle('error',error);}
  function addFields(parent){const root=node('div',null,'cloud-fields');parent.append(root);return root;}
  function field(root,key,title,{type='text',options,help,min,max,step:inputStep}={}){
    const controls=allInputs[key]??=[],wrap=node('label',null,'cloud-field'),titleNode=node('span',title),input=node(options?'select':'input');input.id='cloud-'+key+(controls.length?'-'+controls.length:'');wrap.htmlFor=input.id;titleNode.id=input.id+'-label';input.setAttribute('aria-labelledby',titleNode.id);
    if(options)for(const [value,label]of options){const o=node('option',label);o.value=value;input.append(o);}else input.type=type;
    input.value=raw[key]??normalize(raw)[key];input.spellcheck=false;if(min!=null)input.min=min;if(max!=null)input.max=max;if(inputStep!=null)input.step=inputStep;
    const hint=node('small',help||''),error=node('small','','cloud-error');error.id=input.id+'-error';hint.id=input.id+'-hint';input.setAttribute('aria-describedby',(help?hint.id+' ':'')+error.id);
    input.oninput=()=>{
      if(key==='sizeGB')editedSize=true;if(key==='growthGBPerMonth')raw.growthBasis='custom';raw[key]=input.value;touched.add(key);
      if(key==='provider'){const p=normalize({provider:raw.provider});raw.region=p.region;raw.tier=p.tier;if(p.provider==='azure')raw.retentionMode='days';}
      if(key==='projectMode'&&raw.projectMode==='new'&&!raw.project)raw.project='pharos-backup-'+Math.random().toString(36).slice(2,8);
      if(key==='passwordProtected'){
        if(raw.passwordProtected==='true'&&raw.object===defaults.object)raw.object='pharos/library.pharos';
        else if(raw.passwordProtected==='false'&&raw.object==='pharos/library.pharos')raw.object=defaults.object;
      }
      refresh();
    };
    wrap.append(titleNode,input);if(help)wrap.append(hint);wrap.append(error);root.append(wrap);const control={input,wrap,error,hint};controls.push(control);fields[key]??=control;return input;
  }
  panels[0].append(node('h4','1. Estimate your backup cost'));
  const basicFields=addFields(panels[0]);basicFields.classList.add('cloud-calculator-fields');
  field(basicFields,'provider','Cloud provider',{options:Object.entries(providers).map(([id,p])=>[id,p.name])});
  field(basicFields,'cadence','Backup frequency',{options:[[1,'Daily'],[7,'Weekly'],[30,'Every 30 days']]});
  field(basicFields,'retentionMode','Retain backups by',{options:[['count','Number of backups'],['days','Age in days']]});
  field(basicFields,'keep','Backups to keep',{type:'number',min:1,max:100,step:1});field(basicFields,'retentionDays','Days to keep',{type:'number',min:1,max:3650,step:1});
  const locationSummary=node('p','','cloud-note');panels[0].append(locationSummary);
  const detailed=node('details');detailed.id='cloud-detailed-options';detailed.append(node('summary','Detailed options'));panels[0].append(detailed);
  const costFields=addFields(detailed);
  field(costFields,'region','Storage region',{options:[],help:'Where your backup is stored. Location affects cost and how far your data travels.'});field(costFields,'tier','Storage class',{options:[],help:'How the provider prices storage and recovery. Estimates use your size, growth, frequency and retention.'});
  field(costFields,'minimumAge','Minimum age before pruning (days)',{type:'number',min:1,max:3650,step:1,help:'A grace period after a backup is replaced; it can temporarily retain extra copies.'});
  const billingNote=node('p','','cloud-note cloud-limit');detailed.append(billingNote);
  detailed.append(node('h4','Based on your growing library'));const sizingFields=addFields(detailed);
  field(sizingFields,'sizeGB','Library size (GB)',{type:'number',min:.001,max:1000000,step:'any',help:'Automatically measured. Override to model another library.'});
  field(sizingFields,'compression','Compressed size (% of original)',{type:'number',min:1,max:100,step:'any',help:'Defaults to a fixed 30% of the original size, based on the Euclid library compression sample.'});
  field(sizingFields,'growthGBPerMonth','Monthly library growth (GB)',{type:'number',min:0,max:1000000,step:'any',help:'A fixed amount added each month; it never compounds.'});
  const measurement=node('p','','cloud-note'),growthNote=node('p','','cloud-note'),compressionNote=node('p','','cloud-note');
  const resetGrowth=button('Use automatic growth estimate',()=>{raw.growthBasis=latestGrowth?.basis||'fallback';raw.historyDays=latestGrowth?.history_days||0;raw.growthGBPerMonth=(latestGrowth?.bytes_per_day||0)*30/GB;touched.delete('growthGBPerMonth');refresh();});
  detailed.append(measurement,growthNote,resetGrowth,compressionNote,node('p','The forecast starts with an empty destination and one full backup today. Each revision keeps its upload-day size. It includes storage, upload requests and any early-deletion charges, summed over 365 days. The graph groups charges by calendar month, including partial first and last months. The First month total covers the next full calendar month, including growth and revisions retained since today. First year covers the next 365 days. Taxes, free allowances, discounts, retries, cleanup lag and restore costs are excluded.','cloud-note'));
  const source=node('p',null,'cloud-note'),sourceLink=node('a','Official provider pricing');sourceLink.target='_blank';sourceLink.rel='noopener noreferrer';source.append(sourceLink,document.createTextNode(` · USD price snapshot reviewed ${reviewed}.`));detailed.append(source);

  panels[1].append(node('h4','2. Configure your destination'),node('p','Use a new, dedicated bucket or storage account. Your calculator choices carry through to these settings and the commands.','cloud-note'));
  const configSummary=node('p','','cloud-note');panels[1].append(configSummary,button('Change calculator options',()=>{go(0);detailed.open=true;}));
  const destFields=addFields(panels[1]);field(destFields,'name','Destination name',{help:'A friendly label shown in Pharos, such as “Personal cloud backup.” It helps you recognize this destination.'});field(destFields,'region','Bucket location',{options:[],help:'The region where the provider stores your backup. This is linked to the calculator’s storage region.'});
  field(destFields,'bucket','Bucket name',{help:'The cloud container that holds your backup archive. Must be globally unique; availability is checked when you run the commands.'});
  field(destFields,'profile','AWS CLI profile',{help:'The saved AWS administrator login used to provision the bucket and restricted upload identity.'});
  field(destFields,'projectMode','Google Cloud project',{options:[['existing','Use an existing project'],['new','Create a new project']],help:'A project groups your Google Cloud resources, permissions and charges.'});
  field(destFields,'project','Google Cloud project ID',{help:'The exact ID of an existing project with billing enabled. Find it with gcloud projects list.'});
  field(destFields,'billingAccount','Billing account ID',{help:'An existing active account to pay Google’s storage charges. Find its ID with gcloud billing accounts list. Your signed-in account must be allowed to link it.'});
  const projectNote=node('p','','cloud-note');panels[1].append(projectNote);
  field(destFields,'subscription','Azure subscription',{help:'The existing Azure subscription that pays for these resources. Enter its ID or exact name.'});
  field(destFields,'account','Azure storage account',{help:'The new Azure account holding your backup container. Globally unique, lowercase letters and digits. LRS keeps local copies within one region.'});
  field(destFields,'group','Azure resource group',{help:'A new group used to organize the Azure resources for this backup.'});
  field(destFields,'container','Azure container name',{help:'The container inside the storage account that holds your backup archive.'});
  const classHeading=node('h4','Storage class');panels[1].append(classHeading,node('p','Compare estimated charges at your current settings, including library growth and any fees for deleting revisions before their minimum storage duration.','cloud-note'));const classChoices=node('div',null,'cloud-class-choices');panels[1].append(classChoices);
  panels[1].append(node('h4','Recovery protection'));
  const protectionFields=addFields(panels[1]);protectionFields.classList.add('cloud-protection-fields');field(protectionFields,'passwordProtected','Require a password to restore',{options:[['false','No — restore through my cloud account'],['true','Yes — set a recovery password in Terminal']],help:'Optional. Set and confirm your password in Terminal during setup. Pharos keeps only a public encryption key and a password-encrypted recovery bundle; uploads never ask for the password.'});
  const protectionNote=node('p','','cloud-note');panels[1].append(protectionNote);
  const advanced=node('details');advanced.append(node('summary','Schedule and advanced settings'));const advancedFields=addFields(advanced);
  field(advancedFields,'time','Start at',{type:'time',help:'When a scheduled backup starts, in the time zone below.'});field(advancedFields,'weekday','Day of week',{options:['Sunday','Monday','Tuesday','Wednesday','Thursday','Friday','Saturday'].map((d,i)=>[i,d]),help:'The day to start each weekly backup.'});field(advancedFields,'timezone','Time zone',{help:'The time zone used by the schedule, such as America/Denver.'});
  field(advancedFields,'object','Archive object name',{help:'The filename and optional path inside your bucket, such as pharos/library.tar.zst. Every full backup writes a new revision of this same file; the provider retains older revisions.'});
  field(advancedFields,'identity','Upload identity name',{help:'The new cloud login Pharos will use for unattended uploads: an AWS IAM user or GCP service account. Its credential permits uploading, with delete permission on GCP, and no download or listing access.'});
  field(advancedFields,'projectParent','Project organization or folder (optional)',{help:'For a managed Google Cloud account, enter organizations/123456 or folders/123456. Leave blank for a personal project. Your account must have permission to create projects there.'});
  field(advancedFields,'tokenDays','Azure SAS lifetime (days)',{type:'number',min:1,max:3650,step:1,help:'How long the upload credential remains valid. Replace it before it expires so backups can continue.'});
  const securityNote=node('p','','cloud-note');
  panels[1].append(advanced,node('p','Uploads use the available connection speed, with no speed limit set by Pharos. Actual speed depends on your connection and the cloud provider.','cloud-note'),securityNote);
  const previous=button('Back',()=>go(step-1)),next=button('Configure this backup',()=>{if(step===5)void finishCloudSetup();else go(step+1);},'primary'),actions=node('div',null,'cloud-actions');actions.append(previous,next);const footer=node('div',null,'cloud-footer');footer.append(status,actions);card.append(footer);
  const estimatePanel=node('section',null,'cloud-results'),savedPanel=node('section',null,'cloud-saved');aside.append(estimatePanel);card.append(savedPanel);

  panels[2].append(node('h4','3. Review and run your commands'),node('p','Run these commands to create the destination and private credential files, then connect them in Test. Your upload credential will be stored in the Pharos database.','cloud-note'));
  const review=node('dl',null,'cloud-facts cloud-review');panels[2].append(review);
  const retentionWarnings=[detailed,classChoices,review].map((anchor,i)=>{
    const warning=node('div',null,'cloud-limit cloud-retention-warning');warning.hidden=true;warning.setAttribute('role','status');
    if(i===1)anchor.after(warning);else anchor.before(warning);
    return warning;
  });
  const codeDetails=node('details');codeDetails.open=true;const code=node('pre');codeDetails.append(node('summary','Prepared Bash commands'),code);panels[2].append(codeDetails);
  async function copy(value){try{await navigator.clipboard.writeText(value);message('Copied to clipboard.');}catch{message('Clipboard unavailable. Select and copy the expanded commands.',true);}}
  const protectedCommands=node('div',null,'cloud-protected-commands');protectedCommands.hidden=true;
  const commandStages=[
    ['1. Prepare locally','Copy and run this block first. It prepares private setup files and the password helper. It does not create cloud resources.','Copy preparation commands'],
    ['2. Set your password in Terminal','Paste only this command, then enter and confirm your password. Re-enter it once to verify recovery. Wait for the success message before copying the final block. Keep the password in your password manager; a forgotten password cannot be reset.','Copy password setup command'],
    ['3. Create the cloud destination','After password verification succeeds, copy and run this separate block. It checks the recovery bundle, then creates your cloud resources and upload credential.','Copy final commands']
  ].map(([title,description,label],i)=>{
    const section=node('section',null,'cloud-command-stage'),pre=node('pre'),copyButton=button(label,()=>copy(setupStages(raw)[i].commands));
    section.append(node('h4',title),node('p',description,'cloud-note'),copyButton,pre);protectedCommands.append(section);return{pre,copyButton};
  });panels[2].append(protectedCommands);
  const reviewActions=node('div',null,'cloud-actions'),copyCommands=button('Copy Bash commands',()=>copy(setup(raw)),'primary'),copyPrompt=button('Copy setup prompt',()=>copy(prompt(raw))),copyConfig=button('Copy configuration JSON',()=>copy(JSON.stringify(destination(raw),null,2))),save=button('Save configuration to Pharos',()=>savePlan());reviewActions.append(copyCommands,copyPrompt,copyConfig,save);panels[2].append(reviewActions,node('p','Setup uses your signed-in cloud administrator. If a command fails, repair that step using the private setup files rather than rerunning the whole script.','cloud-note'));
  panels[2].append(node('p','After running the setup commands, continue to Test to connect the private setup folder. Pharos reads the credential locally and stores it directly in its database.','cloud-note'));
  let cloudState={destinations:[],runs:[],schedules:[]},cloudBusy=false,cloudError='',cloudPoll,setupPathEdited=false;
  const terminalCommands=new Map(),scheduleDrafts=new Map(),expandedSchedules=new Set();let selectedRestore='';
  function scheduleFor(d){return (cloudState.schedules||[]).find(s=>s.destination_id===d?.id);}
  function scheduleSpec(input){return {cadence:Number(input.cadence),time:input.time,weekday:Number(input.weekday),timezone:input.timezone};}
  function scheduleStatus(d){
    const s=scheduleFor(d);if(!s?.enabled)return s?.host_id?'Schedule paused':'Schedule not enabled';
    if(s.host_id===cloudState.current_host_id&&s.catalog_path!==cloudState.schedule_catalog)return 'Schedule belongs to another library location · enable it here to use this copy';
    const owner=s.host_id===cloudState.current_host_id?'this Mac':s.host_label||'another Mac';
    let next='';try{next=new Date(s.next_run_at).toLocaleString(undefined,{timeZone:s.timezone,timeZoneName:'short'});}catch{}
    return `${s.in_flight?'Backup running':s.failures?'Retry':'Next backup'}${s.in_flight?'':next?' · '+next:''} · ${owner}`;
  }
  async function updateSchedule(d,enabled,spec=scheduleDrafts.get(d.id)||scheduleFor(d)||d.config){
    cloudBusy=true;cloudError='';refresh();
    try{await cloudRequest('/schedule',{destination_id:d.id,enabled,schedule:scheduleSpec(spec)});scheduleDrafts.delete(d.id);await loadCloud();message(enabled?'Scheduled backups enabled. '+scheduleStatus(d):'Schedule paused. An upload already running can finish; use Cancel to stop it.');}
    catch(error){cloudError=error.message;}
    finally{cloudBusy=false;refresh();}
  }
  async function finishCloudSetup(){
    const d=connectedDestination();if(!d)return;
    if(!scheduleFor(d)?.enabled){await updateSchedule(d,true,raw);return;}
    card.hidden=true;invitation.hidden=false;explore.setAttribute('aria-expanded','false');explore.focus();renderOngoing();
  }
  function openDestination(d,target=4){raw={...normalize(d.config)};editing=d.id;editedSize=true;touched.clear();providerShown='';selectedRestore='';step=target;invitation.hidden=true;card.hidden=false;explore.setAttribute('aria-expanded','true');refresh();panels[target].focus();card.scrollIntoView({block:'nearest'});}
  function renderOngoing(){
    const destinations=cloudState.destinations.filter(d=>d.connected);ongoingPanel.hidden=!card.hidden||!destinations.length;
    if(ongoingPanel.contains(document.activeElement)&&document.activeElement.matches('input,select'))return;
    ongoingPanel.replaceChildren(node('h3','Cloud backups'));
    if(cloudError)ongoingPanel.append(node('p',cloudError,'cloud-status error'));
    const busy=cloudBusy||cloudActive();
    for(const d of destinations){
      const p=normalize(d.config),s=scheduleFor(d),runs=cloudRunsFor(d),latest=runs.find(r=>r.kind==='full'),complete=runs.find(r=>r.kind==='full'&&r.state==='complete');
      const item=node('article');item.setAttribute('aria-label',p.name+' cloud backup');
      item.append(node('h4',p.name),node('p',`${providers[p.provider].name} · ${p.region}`,'cloud-note'),node('p',scheduleStatus(d),'cloud-note'));
      if(complete)item.append(node('p',`Last completed backup: ${new Date(complete.started_at).toLocaleString()} · ${size(complete.total_bytes)}${complete.verified?' · restore verified':''}`,'cloud-note'));
      if(latest&&latest.state!=='complete')item.append(node('p',latest.state==='running'?`${latest.phase}${latest.total_bytes?' · '+size(latest.bytes)+' / '+size(latest.total_bytes):''}`:latest.error||`Latest backup ${latest.state}`,latest.error?'cloud-status error':'cloud-note'));
      if(s?.last_error&&s.last_error!==latest?.error)item.append(node('p',s.last_error,'cloud-status error'));
      const row=node('div',null,'cloud-actions'),now=button('Back up now',()=>startCloud('full',d),'primary'),view=button(complete?'View backups and test restore':'Continue setup',()=>openDestination(d,complete?5:3));now.disabled=busy||!d.sample_verified;row.append(now,view);
      if(s?.enabled){const pause=button('Pause schedule',()=>updateSchedule(d,false));pause.disabled=cloudBusy;row.append(pause);}
      else{const resume=button(s?.host_id?'Resume schedule':'Enable schedule',()=>updateSchedule(d,true));resume.disabled=busy||!d.restore_verified;row.append(resume);}
      if(cloudState.active_run_id&&runs.some(r=>r.id===cloudState.active_run_id)){row.append(button('Cancel backup',async()=>{try{await cloudRequest('/cancel',{run_id:cloudState.active_run_id});await loadCloud();}catch(error){cloudError=error.message;refresh();}}));}
      item.append(row);
      if(!d.restore_verified)item.append(node('p','Complete a full restore test to enable scheduled backups.','cloud-note'));
      const details=node('details',null,'cloud-schedule-settings');details.open=expandedSchedules.has(d.id);details.ontoggle=()=>{if(details.open)expandedSchedules.add(d.id);else expandedSchedules.delete(d.id);};details.append(node('summary','Schedule settings'));
      const grid=node('div',null,'cloud-fields'),draft=scheduleDrafts.get(d.id)||scheduleSpec(s?.host_id?s:p),controls={};
      for(const[key,label,type,options]of [['cadence','Backup frequency','select',[[1,'Daily'],[7,'Weekly'],[30,'Every 30 days']]],['time','Start at','time'],['weekday','Weekly day','select',['Sunday','Monday','Tuesday','Wednesday','Thursday','Friday','Saturday'].map((day,i)=>[i,day])],['timezone','Time zone','text']]){
        const wrap=node('label',null,'cloud-field'),input=node(type==='select'?'select':'input');if(type!=='select')input.type=type;
        if(options)for(const[value,text]of options){const option=node('option',text);option.value=value;input.append(option);}
        input.value=draft[key];input.disabled=busy;controls[key]=input;wrap.append(node('span',label),input);grid.append(wrap);
        input.oninput=()=>{const values={};for(const[k,c]of Object.entries(controls))values[k]=c.value;scheduleDrafts.set(d.id,scheduleSpec(values));};
      }
      const apply=button(s?.enabled&&(s.host_id!==cloudState.current_host_id||s.catalog_path!==cloudState.schedule_catalog)?'Run schedule here':s?.enabled?'Update schedule':'Enable schedule with these settings',()=>updateSchedule(d,true,scheduleDrafts.get(d.id)||draft));apply.disabled=busy||!d.restore_verified;
      details.append(grid,node('p','Schedules are bound to this Mac and library location, and run while Pharos is running with the library drive connected. Sleep or downtime produces one catch-up backup. Times follow the selected time zone; every 30 days is anchored to the date you enable or change the schedule. Scheduled failures retry after 1 hour, 6 hours and 24 hours, then wait for the next regular date. Pausing stops future runs. Changing frequency does not change the bucket’s retention rules.','cloud-note'),apply);item.append(details);ongoingPanel.append(item);
    }
  }
  const operationKeys=['provider','region','name','object','storage_class','passwordProtected','cadence','keep','minimumAge','retentionMode','retentionDays','time','weekday','timezone'];
  function destinationFor(input=raw){
    const expected=destination(input),keys=[...operationKeys,...(expected.provider==='azure'?['account','container']:['bucket','identity']),...(expected.provider==='gcp'?['project']:[]),...(expected.provider==='aws'?['profile']:[])];
    return cloudState.destinations.find(d=>keys.every(key=>d.config[key]===expected[key]));
  }
  function connectedDestination(input=raw){const d=destinationFor(input);return d?.connected?d:null;}
  function cloudRunsFor(d){return d?cloudState.runs.filter(r=>r.destination_id===d.id):[];}
  async function cloudRequest(action,data){const response=await fetch('/api/cloud-backup'+action,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(data)});const result=await response.json();if(!response.ok)throw new Error(result.error||'Cloud operation failed');return result;}
  function cloudActive(){return Boolean(cloudState.active_run_id)||cloudState.runs.some(r=>r.verification_state==='running'&&r.verification_host===cloudState.current_host_id);}
  async function loadCloud(){
    clearTimeout(cloudPoll);if(document.hidden)return;
    try{const response=await fetch('/api/cloud-backup');const result=await response.json();if(!response.ok)throw new Error(result.error||'Cloud backup status is unavailable');cloudState=result;cloudError='';renderSaved();}catch(error){cloudError=error.message;}
    refresh();renderOngoing();const active=cloudActive();cloudPoll=setTimeout(loadCloud,active?1500:10000);
  }
  document.addEventListener('visibilitychange',()=>{if(!document.hidden)void loadCloud();});
  async function connectCloud(){
    cloudBusy=true;cloudError='';refresh();
    try{await cloudRequest('/import',{setup_directory:setupFolder.value,expected:destination(raw)});await loadCloud();message('Connected. Restricted upload credential saved in the Pharos database.');}catch(error){cloudError=error.message;}finally{cloudBusy=false;refresh();}
  }
  async function startCloud(kind,selected){
    const d=selected||connectedDestination();if(!d)return;cloudBusy=true;cloudError='';refresh();
    try{const run=await cloudRequest('/start',{destination_id:d.id,kind});cloudState.runs.unshift(run);await loadCloud();}catch(error){cloudError=error.message;}finally{cloudBusy=false;refresh();}
  }
  function resultBox(parent){const box=node('div',null,'cloud-run-result');box.setAttribute('role','status');box.hidden=true;parent.append(box);return box;}
  function showResult(box,title,description,error=false){box.hidden=!title;box.classList.toggle('success',Boolean(title)&&!error);box.classList.toggle('error',error);box.replaceChildren();if(title)box.append(node('strong',title),document.createTextNode(description));}
  function checks(parent,items,complete){parent.replaceChildren();for(const text of items)parent.append(node('li',text,complete?'complete':''));}
  function latestRun(kind){return cloudRunsFor(destinationFor()).find(r=>r.kind===kind);}
  function restoreRun(){const runs=cloudRunsFor(destinationFor()).filter(r=>r.kind==='full'&&r.state==='complete');return runs.find(r=>r.id===selectedRestore)||runs[0];}
  function receiptFor(run){return run?{kind:run.kind,object:run.object,version:run.version}:null;}
  function archivePath(view,kind){const folder=view.input.value.trim().replace(/\/+$/,'');return folder+'/'+(kind==='sample'?'round-trip-test.pharos':normalize(raw).passwordProtected?'library.pharos':'library.tar.zst');}
  async function verifyCloud(kind){
    const run=kind==='sample'?latestRun(kind):restoreRun(),view=kind==='sample'?sampleDownload:fullDownload;if(!run)return;cloudBusy=true;cloudError='';refresh();
    try{const result=await cloudRequest('/verify',{run_id:run.id,archive_path:archivePath(view,kind)});if(result.terminal_required){terminalCommands.set(run.id,{path:archivePath(view,kind),command:result.command});message('Copy the standalone command, run it in Terminal and enter your recovery password. This page will update when verification finishes.');}else{run.verified=false;run.verification_state='running';await loadCloud();}}catch(error){cloudError=error.message;}finally{cloudBusy=false;refresh();}
  }
  function downloadSection(parent,kind){
    const section=node('section',null,'cloud-run-block');section.append(node('h4',kind==='sample'?'Download the test file':'Download this backup revision'),node('p','Run this command using your cloud account with download access. It retrieves the exact uploaded revision. Pharos’s stored upload credential cannot download it.','cloud-note'));
    const wrap=node('label',null,'cloud-field'),input=node('input');input.type='text';input.spellcheck=false;input.value='~/Downloads/Pharos Restore/'+(kind==='sample'?'Test':'First backup');
    wrap.append(node('span','Download folder'),input,node('small','Choose an empty folder on this Mac. If you move the downloaded file, update this folder before verifying it.'));
    const pre=node('pre'),copyButton=button('Copy download command',()=>copy(restoreDownload(raw,receiptFor(kind==='full'?restoreRun():latestRun(kind)),input.value)));
    input.oninput=()=>{const run=kind==='full'?restoreRun():latestRun(kind);if(run)terminalCommands.delete(run.id);refresh();};
    const commands=node('details');commands.append(node('summary','View download command'),pre);section.append(wrap,copyButton,commands);parent.append(section);return{section,input,pre,copyButton};
  }
  function terminalSection(parent,kind){
    const block=node('section',null,'cloud-run-block');block.hidden=true;block.append(node('h4','Enter your password in Terminal'),node('p','Run this command on its own after the download finishes. Enter your recovery password when Terminal asks. It is never sent to this page or saved by Pharos.','cloud-note'));
    const pre=node('pre'),copyButton=button('Copy password verification command',()=>copy(terminalCommands.get((kind==='full'?restoreRun():latestRun(kind))?.id)?.command||''));block.append(copyButton,pre);parent.append(block);return{block,pre};
  }
  panels[3].append(node('h4','4. Test your cloud backup'),node('p','Connect the destination, upload a small test archive, then download it through your cloud account and verify a complete recovery round trip.','cloud-note'));
  const librarySourceNotes=[3,4,5].map(()=>node('p','','cloud-note cloud-limit'));
  const connectBlock=node('section',null,'cloud-run-block'),setupLabel=node('label',null,'cloud-field'),setupFolder=node('input');setupFolder.type='text';setupFolder.spellcheck=false;setupFolder.oninput=()=>{setupPathEdited=true;};setupLabel.append(node('span','Private setup folder'),setupFolder);
  const connectButton=button('Connect destination',connectCloud,'primary'),connectionResult=resultBox(connectBlock);
  connectBlock.append(node('h4','Connect your setup files'),setupLabel,node('p','Use the folder printed by the setup commands. Pharos reads the credential file directly; it never passes through your browser. Connecting uploads a tiny random probe to the test object and checks that read/list access is forbidden before storing the credential. A cloud provider may charge a very small amount for the probe.','cloud-note'),connectButton);panels[3].append(connectBlock);
  const cloudErrorNode=resultBox(panels[3]);
  const testLayout=node('div',null,'cloud-run-layout'),testMain=node('div'),testMeta=node('aside',null,'cloud-run-metadata');testLayout.append(testMain,testMeta);panels[3].append(testLayout);
  testMain.append(node('h4','Upload a small test archive'),node('p','The 1 MiB sample uses the same archive format and encryption as the full backup. It has its own test object and does not replace the library archive.','cloud-note'));
  const testUpload=button('Upload test archive',()=>startCloud('sample'),'primary');testMain.append(testUpload);const testUploadResult=resultBox(testMain),sampleDownload=downloadSection(testMain,'sample');
  const sampleVerify=node('section',null,'cloud-run-block'),samplePasswordNote=node('p','','cloud-note');sampleVerify.append(node('h4','Verify the round trip'),samplePasswordNote);
  const sampleVerifyButton=button('Verify downloaded test',()=>verifyCloud('sample'),'primary');sampleVerify.append(sampleVerifyButton);const sampleTerminal=terminalSection(sampleVerify,'sample'),sampleVerifyResult=resultBox(sampleVerify),sampleChecks=node('ul',null,'cloud-run-checks');sampleVerify.append(sampleChecks);testMain.append(sampleVerify);
  panels[4].append(node('h4','5. Make your first full backup'),node('p','Start a complete backup immediately. You can also make another backup here whenever you need one, independently of your schedule.','cloud-note'));
  const backupLayout=node('div',null,'cloud-run-layout'),backupMain=node('div'),backupMeta=node('aside',null,'cloud-run-metadata');backupLayout.append(backupMain,backupMeta);panels[4].append(backupLayout);
  const backupPrerequisite=node('p','','cloud-note cloud-limit'),backupProtection=node('p','','cloud-note');backupMain.append(backupPrerequisite,node('h4','Back up the complete library'),backupProtection);
  const firstBackup=button('Start first full backup now',()=>startCloud('full'),'primary');backupMain.append(firstBackup);const backupResult=resultBox(backupMain),backupChecks=node('ul',null,'cloud-run-checks');backupMain.append(backupChecks,node('p','The snapshot and compressed archive are staged locally during upload. Keep the library drive connected. Cancellation removes temporary files; it never deletes a completed cloud revision.','cloud-note'));
  panels[5].append(node('h4','6. Test a full library restore'),node('p','Download the completed backup revision and validate it in isolation against the manifest captured during that backup. Later library changes will not affect this comparison.','cloud-note'));
  const restoreLayout=node('div',null,'cloud-run-layout'),restoreMain=node('div'),restoreMeta=node('aside',null,'cloud-run-metadata');restoreLayout.append(restoreMain,restoreMeta);panels[5].append(restoreLayout);
  const restorePrerequisite=node('p','','cloud-note cloud-limit');restoreMain.append(restorePrerequisite);const revisionLabel=node('label',null,'cloud-field'),revisionSelect=node('select',null,'cloud-history-select');revisionLabel.append(node('span','Backup revision'),revisionSelect);revisionSelect.onchange=()=>{selectedRestore=revisionSelect.value;refresh();};restoreMain.append(revisionLabel);const fullDownload=downloadSection(restoreMain,'full'),restorePasswordNote=node('p','','cloud-note');
  const restoreVerify=node('section',null,'cloud-run-block');restoreVerify.append(node('h4','Validate the recovered library'),restorePasswordNote);
  const restoreVerifyButton=button('Validate downloaded backup',()=>verifyCloud('full'),'primary');restoreVerify.append(restoreVerifyButton);const restoreTerminal=terminalSection(restoreVerify,'full'),restoreResult=resultBox(restoreVerify),restoreChecks=node('ul',null,'cloud-run-checks');restoreVerify.append(restoreChecks,node('p','Pharos extracts into a new private staging folder. It rejects links and unsafe paths, checks SQLite integrity, and compares every file’s size and SHA-256 with the backup manifest. The live library is never replaced.','cloud-note'));restoreMain.append(restoreVerify);
  const scheduleSetup=node('p','','cloud-note cloud-limit');panels[5].append(scheduleSetup);
  const executionError=resultBox(panels[4]),restoreError=resultBox(panels[5]);
  librarySourceNotes.forEach((note,i)=>panels[i+3].insertBefore(note,panels[i+3].children[2]||null));
  const cancelCloudButton=button('Cancel current operation',async()=>{const id=cloudState.active_run_id;if(!id)return;try{await cloudRequest('/cancel',{run_id:id});await loadCloud();}catch(error){cloudError=error.message;refresh();}});actions.prepend(cancelCloudButton);
  function refreshWorkflow(p){
    const d=connectedDestination(),sample=latestRun('sample'),backup=latestRun('full'),full=restoreRun(),active=cloudActive();
    workflow={setup:Boolean(d),sample,full:full?.state==='complete'?full:null,sampleVerified:Boolean(d?.sample_verified||sample?.verified),restoreVerified:Boolean(d?.restore_verified||full?.verified)};
    const busy=cloudBusy||active;
    for(const note of librarySourceNotes)note.textContent=cloudState.library_catalog?`${document.title.includes('(dev)')?'Development UI · b':'B'}ackups use the service library at ${cloudState.library_catalog}`:'Loading the active library location…';
    if(!setupPathEdited)setupFolder.value=(cloudState.setup_root||'~/Library/Application Support/Pharos/cloud-setup')+'/'+p.provider+'/'+(p.provider==='azure'?p.account:p.bucket);
    setupFolder.disabled=cloudBusy;connectButton.disabled=busy||Boolean(d);connectButton.textContent=cloudBusy&&!d?'Connecting…':d?'Destination connected':'Connect destination';
    showResult(connectionResult,d?'Connected':'',d?'Restricted upload credential is stored in the Pharos database. Read and list access checks were forbidden.':'');
    for(const box of [cloudErrorNode,executionError,restoreError])showResult(box,cloudError?'Cloud operation needs attention':'',cloudError,true);
    testUpload.disabled=busy||!d;testUpload.textContent=sample?.state==='running'&&sample.owner_host===cloudState.current_host_id?'Uploading test…':'Upload test archive';
    function uploadResult(box,run,label){const complete=run?.state==='complete';const description=complete?`${size(run.total_bytes)} uploaded. Revision: ${run.version}`:run?.error||run?.phase||'';showResult(box,run?(complete?label+' uploaded':run.state==='running'?run.phase:label+' '+run.state):'',description,Boolean(run?.error));}
    uploadResult(testUploadResult,sample,'Test archive');uploadResult(backupResult,backup,'Full backup');
    for(const [view,run]of [[sampleDownload,sample],[fullDownload,full]]){const ready=run?.state==='complete';view.section.hidden=!ready;view.copyButton.disabled=!ready;if(ready){try{view.pre.textContent=restoreDownload(p,receiptFor(run),view.input.value);view.copyButton.disabled=false;}catch(error){view.pre.textContent=error.message;view.copyButton.disabled=true;}}}
    sampleVerify.hidden=sample?.state!=='complete';restoreVerify.hidden=full?.state!=='complete';
    sampleVerifyButton.disabled=busy||sample?.state!=='complete';restoreVerifyButton.disabled=busy||full?.state!=='complete';
    sampleVerifyButton.textContent=p.passwordProtected?'Get Terminal verification command':'Verify downloaded test';restoreVerifyButton.textContent=p.passwordProtected?'Get Terminal verification command':'Validate downloaded backup';
    samplePasswordNote.textContent=p.passwordProtected?'After downloading, run the separate verification command in Terminal and enter your password again. Setup password verification does not count as a recovery test.':'Verify the downloaded file’s archive digest and original test bytes.';
    restorePasswordNote.textContent=p.passwordProtected?'The verification command asks for your password in Terminal, decrypts this downloaded archive and records the full validation result here.':'Validate the downloaded file against this backup’s archive digest and snapshot manifest.';
    for(const [section,run,view]of [[sampleTerminal,sample,sampleDownload],[restoreTerminal,full,fullDownload]]){const cmd=terminalCommands.get(run?.id);section.block.hidden=!cmd||cmd.path!==archivePath(view,run?.kind);section.pre.textContent=cmd?.command||'';}
    function verificationResult(box,run,label){showResult(box,run?.verified?label+' verified':run?.verification_state==='running'?'Verifying downloaded archive':run?.verification_error?'Verification failed':'',run?.verified?`${run.files} files matched. Recovered copy: ${run.restored_directory}`:run?.verification_error||'Checking the archive and recovered files…',Boolean(run?.verification_error));}
    verificationResult(sampleVerifyResult,sample,'Round trip');verificationResult(restoreResult,full,'Full restore');
    checks(sampleChecks,['Downloaded archive matches the uploaded SHA-256',...(p.passwordProtected?['Recovery bundle unlocked with a fresh Terminal password entry','Payload decrypted and authenticated']:[]),'Test bytes match the original sample'],sample?.verified);
    testMeta.replaceChildren(node('h4','Test details'));facts(testMeta.appendChild(node('dl',null,'cloud-facts')),[['Destination',p.name],['Sample payload','1 MiB'],['Object',p.object+'.roundtrip-test'],['Protection',p.passwordProtected?'Password-protected archive':'Cloud-managed encryption'],['Download access','Your own cloud login, used in Terminal'],['Download availability',rate(p).latency]]);
    backupPrerequisite.textContent=workflow.sampleVerified?'The downloaded test has been verified. You can start the full backup now.':'Connect the destination and verify a downloaded test before the first full backup.';
    firstBackup.disabled=busy||!d||!workflow.sampleVerified;firstBackup.textContent=backup?.state==='running'&&backup.owner_host===cloudState.current_host_id?'Backing up…':workflow.full?'Back up now':'Start first full backup now';
    backupProtection.textContent='Pharos makes a consistent catalog snapshot and copies the library data, then compresses the complete archive'+(p.passwordProtected?' and encrypts it with the public key. Uploads never ask for your password.':'.');
    const estimateNow=estimate(p),t=rate(p);
    backupMeta.replaceChildren(node('h4','Latest backup'));const backupInfo=backup||full;facts(backupMeta.appendChild(node('dl',null,'cloud-facts')),[['Library size estimate',size(p.sizeGB*GB)],['Archive size',backupInfo?.total_bytes?size(backupInfo.total_bytes):size(estimateNow.snapshotBytes)+' estimated'],['Uploaded',backupInfo?.total_bytes?`${size(backupInfo.bytes)} / ${size(backupInfo.total_bytes)}`:'Not started'],['Estimated staging space',size(p.sizeGB*GB+estimateNow.snapshotBytes)],['Protection',p.passwordProtected?'Password-protected archive':'Cloud-managed encryption'],['Schedule',scheduleStatus(d)]]);
    checks(backupChecks,['Consistent catalog and library snapshot','Full archive compressed'+(p.passwordProtected?' and encrypted':''),'All upload parts committed as one object revision','Version ID, archive digest and file manifest recorded'],backup?.state==='complete');
    const revisions=cloudRunsFor(destinationFor()).filter(r=>r.kind==='full'&&r.state==='complete');revisionSelect.replaceChildren(...revisions.map(run=>{const option=node('option',new Date(run.started_at).toLocaleString()+' · '+(run.trigger==='scheduled'?'Scheduled':'Manual')+(run.verified?' · restore verified':''));option.value=run.id;return option;}));revisionSelect.value=full?.id||'';revisionLabel.hidden=!revisions.length;
    const configuredSchedule=scheduleFor(d);scheduleSetup.textContent=configuredSchedule?.enabled?scheduleStatus(d):workflow.restoreVerified?`Ready to enable ${schedule(p)} backups on this Mac. Pharos must be running with the library drive connected. Missed runs produce one catch-up backup; scheduled failures retry after 1 hour, 6 hours and 24 hours.`:'A successful full restore test is required before enabling automatic backups.';
    restorePrerequisite.textContent=workflow.full?'The download command is pinned to the completed backup revision.':'Complete the first full backup to obtain its download command.';
    checks(restoreChecks,['Archive digest matches the uploaded revision',...(p.passwordProtected?['Recovery password unlocked this archive','Payload decrypted and authenticated']:[]),'Archive contains only safe file paths','Recovered SQLite catalog passes integrity_check','Configuration and catalog are present','Every file matches its snapshot size and SHA-256'],full?.verified);
    restoreMeta.replaceChildren(node('h4','Restore details'));facts(restoreMeta.appendChild(node('dl',null,'cloud-facts')),[['Revision',full?.version||'Available after upload'],['Download',full?.total_bytes?size(full.total_bytes):size(estimateNow.snapshotBytes)+' estimated'],['Files',full?.files?number(full.files):'Recorded during backup'],['Free space for recovered copy',full?.file_bytes?size(full.file_bytes):size(p.sizeGB*GB)],['Storage access',t.latency],['Estimated retrieval + transfer',money(estimateNow.retrievalCost+estimateNow.egressCost)+' once; request fees excluded'],['Live library','Kept separate from verification output']]);
    cancelCloudButton.hidden=!cloudState.active_run_id;
  }
  const facts=(root,entries)=>{root.replaceChildren();for(const [label,value]of entries){const d=node('div');d.append(node('dt',label),node('dd',value));root.append(d);}};
  function schedule(p){return `${p.cadence===1?'Daily':p.cadence===7?'Every '+['Sunday','Monday','Tuesday','Wednesday','Thursday','Friday','Saturday'][p.weekday]:'Every 30 days'} at ${p.time} · ${p.timezone}`;}
  function retention(p){return p.retentionMode==='count'?`${p.keep} completed backups`:`Previous versions expire after ${p.retentionDays} days`;}
  function showErrors(errors){for(const[key,controls]of Object.entries(allInputs))for(const c of controls){const msg=submitted||touched.has(key)?errors[key]||'':'';c.error.textContent=msg;c.input.setAttribute('aria-invalid',String(Boolean(msg)));}}
  function go(target){
    if(target<0||target>=panels.length)return;
    hideChartTooltip();
    if(target>step){submitted=true;const errors=validation(raw,target===1?0:2);showErrors(errors);if(Object.keys(errors).length){message('Complete the highlighted fields.',true);const controls=allInputs[Object.keys(errors)[0]]||[],first=(controls.find(c=>c.input.closest('.cloud-step')===panels[step])||controls[0])?.input;if(first){const index=panels.indexOf(first.closest('.cloud-step'));if(index>=0){step=index;refresh();}for(let d=first.closest('details');d;d=d.parentElement?.closest('details'))d.open=true;first.focus();}return;}}
    step=target;submitted=false;message('');refresh();panels[step].focus({preventScroll:true});mount.scrollIntoView({block:'start'});
  }
  function resetDraft(){
    if(saving)return;
    raw=newDraft();step=0;submitted=false;editedSize=false;editing='';selectedMonth=0;touched.clear();workflow=emptyWorkflow();workflowPlan='';workflowNotice='';
    detailed.open=false;advanced.open=false;codeDetails.open=true;
    if(latestLibraryBytes>0)window.pharosCloudBackup.seedLibrary(latestLibraryBytes,latestGrowth);
    refresh();message('Options reset to defaults. Saved destinations are unchanged.');panels[0].focus({preventScroll:true});
  }
  function graph(label,comparison){
    const figure=node('figure',null,'cloud-chart');figure.append(node('figcaption',label));
    const months=comparison.projected.months,flat=comparison.flat.months,last=months.length-1;
    const components=[['storageCost','Storage','storage'],['uploadCost','Uploads','upload']];
    if(comparison.projected.minimumStorageCost>0||comparison.flat.minimumStorageCost>0)components.push(['minimumStorageCost','Early-deletion fees','minimum']);
    const svgNode=(tag,attrs={},text)=>{const el=document.createElementNS('http://www.w3.org/2000/svg',tag);for(const[k,v]of Object.entries(attrs))el.setAttribute(k,v);if(text!=null)el.textContent=text;return el;};
    const peak=Math.max(...months.map(m=>m.cost),...flat.map(m=>m.cost),1e-8),magnitude=10**Math.floor(Math.log10(peak)),ceiling=Math.ceil(peak/magnitude)*magnitude;
    const width=Math.max(200,estimatePanel.clientWidth),height=126,left=Math.max(54,money(ceiling).length*7+12),right=8,top=10,bottom=26,plotW=width-left-right,plotH=height-top-bottom,slot=plotW/months.length,barWidth=slot*.32;
    const x=i=>left+(i+.5)*slot,y=value=>top+plotH*(1-value/ceiling);
    const describe=m=>components.map(([key,name])=>`${name} ${formatCost(m[key])}`).join(' · ');
    const svg=svgNode('svg',{viewBox:`0 0 ${width} ${height}`,role:'img',tabindex:'0','aria-describedby':chartTooltip.id,'aria-label':`${label}. Paired stacked bars show storage, upload request fees and any early-deletion fees. Solid: no growth. Dotted outline: with growth. Use left and right arrow keys to explore months, up and down to switch growth scenarios, and Escape to dismiss the cost popover.`});
    for(const fraction of [0,.5,1]){const value=fraction*ceiling,yy=y(value);svg.append(svgNode('line',{x1:left,y1:yy,x2:width-right,y2:yy,class:'cloud-chart-grid'}),svgNode('text',{x:left-8,y:yy+4,'text-anchor':'end',class:'cloud-chart-tick'},money(value)));}
    for(const i of (width<280?[0,last]:[0,Math.floor(last/2),last]))svg.append(svgNode('text',{x:i===0?left:i===last?width-right:x(i),y:height-5,'text-anchor':i===0?'start':i===last?'end':'middle',class:'cloud-chart-tick'},months[i].label));
    const cursor=svgNode('rect',{y:top,width:slot,height:plotH,class:'cloud-chart-cursor'});svg.append(cursor);
    for(let i=0;i<months.length;i++)for(const[scenario,month,offset]of [['flat',flat[i],-.36],['projected',months[i],.04]]){
      const group=svgNode('g',{class:'cloud-bar-'+scenario,'data-cloud-month':i,'data-cloud-scenario':scenario,'aria-label':`${month.label}, ${scenario==='flat'?'no growth':'with growth'}: ${describe(month)} · Total ${formatCost(month.cost)}`}),xx=x(i)+slot*offset;let stack=0;
      group.append(svgNode('rect',{x:left+i*slot+(scenario==='flat'?0:slot/2),y:top,width:slot/2,height:plotH,fill:'transparent'}));
      for(const[key,,cls]of components){const value=month[key];if(value>0)group.append(svgNode('rect',{x:xx,y:y(stack+value),width:barWidth,height:plotH*value/ceiling,class:'cloud-bar-'+cls}));stack+=value;}
      group.append(svgNode('rect',{x:xx,y:y(month.cost),width:barWidth,height:plotH*month.cost/ceiling,class:'cloud-bar-total'}));svg.append(group);
    }
    figure.append(svg);
    const readout=node('output','','cloud-chart-readout');readout.setAttribute('aria-live','polite');readout.setAttribute('aria-atomic','true');figure.append(readout);let selected=-1;
    function show(i){
      const next=Math.max(0,Math.min(last,i));if(next===selected)return;selected=next;
      selectedMonth=selected;
      cursor.setAttribute('x',left+selected*slot);readout.replaceChildren(node('strong',`${months[selected].label} · ${months[selected].days} forecast days`));
      for(const[name,month]of [['With growth',months[selected]],['No growth',flat[selected]]]){const row=node('span',null,'cloud-cost-row');row.append(node('b',`${name}: ${formatCost(month.cost)} total`),node('span',describe(month)));readout.append(row);}
    }
    let tipScenario='projected';
    function reveal(i,scenario,anchor){
      show(i);tipScenario=scenario;
      const month=scenario==='flat'?flat[selected]:months[selected];
      chartTooltip.replaceChildren(node('strong',month.label),node('span',`${scenario==='flat'?'No growth':'With growth'} · ${month.days} forecast days`,'chart-tooltip-context cloud-cost-tooltip-context'));
      for(const[key,name,cls]of [...components,['cost','Total',null]]){
        const row=node('span',null,'chart-tooltip-row'+(key==='cost'?' cloud-cost-tooltip-total':''));
        if(cls){const swatch=node('i',null,'chart-tooltip-swatch cloud-legend-'+cls);swatch.setAttribute('aria-hidden','true');row.append(swatch);}
        row.append(node('span',name),node('span',formatCost(month[key]),'cloud-cost-tooltip-value'));chartTooltip.append(row);
      }
      chartTooltip.hidden=false;
      const rect=svg.getBoundingClientRect(),bounds=chartTooltip.getBoundingClientRect();
      const ax=anchor?.x??rect.left+(x(selected)+(scenario==='flat'?-.2:.2)*slot)*rect.width/width;
      const ay=anchor?.y??rect.top+y(month.cost)*rect.height/height;
      chartTooltip.style.left=Math.max(8,Math.min(ax+12,window.innerWidth-bounds.width-8))+'px';
      const below=ay+16,above=ay-bounds.height-12;
      chartTooltip.style.top=Math.max(8,Math.min(below+bounds.height>window.innerHeight-8?above:below,window.innerHeight-bounds.height-8))+'px';
    }
    const point=event=>{
      if(event.type==='pointermove'&&event.pointerType==='touch')return;
      const mark=event.target.closest('[data-cloud-month]');
      if(!mark||!svg.contains(mark)){hideChartTooltip();return;}
      reveal(Number(mark.dataset.cloudMonth),mark.dataset.cloudScenario,{x:event.clientX,y:event.clientY});
    };
    svg.onpointermove=point;svg.onclick=point;svg.onpointerleave=event=>{if(event.pointerType!=='touch')hideChartTooltip();};svg.onblur=hideChartTooltip;svg.onfocus=()=>reveal(selected,tipScenario);
    svg.onkeydown=event=>{
      const i={ArrowLeft:selected-1,ArrowRight:selected+1,Home:0,End:last}[event.key];
      if(i!=null){event.preventDefault();reveal(i,tipScenario);}
      else if(event.key==='ArrowUp'||event.key==='ArrowDown'){event.preventDefault();reveal(selected,event.key==='ArrowUp'?'projected':'flat');}
      else if(event.key==='Escape'){event.preventDefault();hideChartTooltip();}
    };
    show(selectedMonth);
    return figure;
  }
  function renderEstimate(comparison,invalid){
    const e=comparison.projected,wasFocused=estimatePanel.contains(document.activeElement);hideChartTooltip();chartWidth=Math.round(estimatePanel.clientWidth);estimatePanel.replaceChildren();
    const totals=node('div',null,'cloud-totals');for(const[label,value]of [[`First month · ${e.firstMonthLabel}`,e.firstMonth],['First year',e.year]]){const total=node('div');total.append(node('span',label),node('strong',invalid?'—':money(value)));totals.append(total);}estimatePanel.append(totals);
    if(invalid){estimatePanel.append(node('p','Enter valid values in Detailed options to see the forecast.','cloud-note'));return;}
    const legend=node('div',null,'cloud-chart-legend'),scenarios=node('div',null,'cloud-chart-legend cloud-chart-scenarios');
    const categories=[['Storage','storage'],['Uploads','upload']];if(e.minimumStorageCost>0||comparison.flat.minimumStorageCost>0)categories.push(['Early-deletion fees','minimum']);
    for(const[root,items]of [[legend,categories],[scenarios,[['No growth','flat'],['With growth','projected']]]])for(const[label,cls]of items){const item=node('span');item.append(node('i',null,'cloud-legend-'+cls),document.createTextNode(label));root.append(item);}
    estimatePanel.append(legend,scenarios,graph('Price per month',comparison),node('p','Upload costs are provider request fees; very small amounts may not be visible in the bars. Hover, tap or use arrow keys for exact costs. Estimates are charges from your cloud provider.','cloud-note'));
    if(wasFocused)estimatePanel.querySelector('svg').focus({preventScroll:true});
  }
  function refresh(){
    ongoingPanel.hidden=!card.hidden||!cloudState.destinations.some(d=>d.connected);
    const p=normalize(raw),t=rate(p),comparison=projection(p),e=comparison.projected;
    const errors=validation(raw,step===0?0:2),pricingErrors=validation(raw,0),validPricing=Object.keys(pricingErrors).length===0;
    reset.disabled=saving;
    refreshWorkflow(p);
    for(let i=0;i<panels.length;i++){panels[i].hidden=i!==step;navButtons[i].setAttribute('aria-current',i===step?'step':'false');}layout.classList.toggle('cloud-layout-wide',step>=2);aside.hidden=step>=2;previous.hidden=step===0;
    next.textContent=['Configure this backup','Review configuration & commands','Continue to Test','Continue to First backup','Continue to Test restore',scheduleFor(connectedDestination())?.enabled?'Finish setup':'Enable scheduled backups'][step];
    next.disabled=saving||cloudBusy||(step===3&&!workflow.sampleVerified)||(step===4&&!workflow.full)||(step===5&&!workflow.restoreVerified);
    if(providerShown!==p.provider){
      for(const {input}of allInputs.region){input.replaceChildren();for(const region of regions[p.provider]){const o=node('option',`${region.label} · ${region.id}`);o.value=region.id;input.append(o);}}
      const select=fields.tier.input;select.replaceChildren();for(const candidate of tiers.filter(c=>c.provider===p.provider)){const o=node('option',candidate.name+(candidate.minimumDays?` · ${candidate.minimumDays}-day minimum`:' · flexible billing'));o.value=candidate.id;o.disabled=candidate.id==='azure-archive';if(o.disabled)o.textContent+=' · unavailable';select.append(o);}providerShown=p.provider;
    }
    for(const[key,controls]of Object.entries(allInputs))for(const c of controls){if(document.activeElement!==c.input)c.input.value=key==='growthGBPerMonth'?p.growthGBPerMonth:(raw[key]??p[key]);}
    for(const key of ['bucket','profile','projectMode','project','billingAccount','projectParent','subscription','account','group','container','identity','keep','minimumAge','retentionDays','tokenDays','weekday']){
      const newProject=p.provider==='gcp'&&p.projectMode==='new';
      const visible={bucket:p.provider!=='azure',profile:p.provider==='aws',projectMode:p.provider==='gcp',project:p.provider==='gcp',billingAccount:newProject,projectParent:newProject,subscription:p.provider==='azure',account:p.provider==='azure',group:p.provider==='azure',container:p.provider==='azure',identity:p.provider!=='azure',keep:p.retentionMode==='count',minimumAge:p.retentionMode==='count',retentionDays:p.retentionMode==='days',tokenDays:p.provider==='azure',weekday:p.cadence===7};for(const c of allInputs[key])c.wrap.hidden=!visible[key];
    }
    fields.project.hint.textContent=p.projectMode==='new'?'A globally unique ID for the project the commands will create. The suggested ID is editable; Google checks availability when you run the commands.':'The exact ID of an existing project with billing enabled. Find it with gcloud projects list.';
    protectionNote.textContent=p.passwordProtected?'You will need your cloud account and this password to restore. Every backup must include its encrypted recovery bundle, so there is no separate key file to keep. Losing the password makes those backups unrecoverable. Password setup requires age (on macOS: brew install age).':'Cloud-managed encryption is still enabled. Recovery requires your cloud account; there is no archive password or separate key file.';
    securityNote.textContent='The upload credential will be stored in Pharos’s database. '+(p.passwordProtected?'The archive will also be encrypted before upload; only the public key and encrypted recovery bundle are kept.':'Cloud-managed encryption; restore through your cloud account. No backup password or separate recovery key.');
    projectNote.hidden=p.provider!=='gcp'||p.projectMode!=='new';projectNote.textContent='Commands will create the project, link the billing account above and enable Storage and IAM APIs. Google checks name availability, project quota and your permissions. If any step fails, setup stops. An active billing account is required; create one in Google Cloud first if needed.';
    fields.retentionMode.input.querySelector('option[value="count"]').disabled=p.provider==='azure';
    const frequency=p.cadence===1?'Daily':p.cadence===7?'Weekly':'Every 30 days';
    locationSummary.textContent=`${providers[p.provider].name} ${t.name} · ${t.region.label}. ${t.minimumDays?`Early-deletion fees if a revision is removed before ${t.minimumDays} days.`:'Flexible storage billing, with immediate access for recovery.'}`;
    billingNote.textContent=(p.provider==='gcp'?'GCS has no minimum object size. ':'')+(t.minimumDays?`Each revision has a ${t.minimumDays}-day minimum storage duration. You may delete it earlier, but the provider bills the remaining days as an early-deletion fee. No capacity reservation is needed.`:'There is no minimum storage duration or capacity reservation to configure.')+(p.provider==='gcp'&&t.minimumDays?' With versioning, uploading a new revision does not itself trigger this fee; deleting an older revision before it reaches the minimum duration does.':'')+` Restore access: ${t.latency}.`+(p.provider==='azure'?' Azure supports age-based retention.':'');
    for(const warning of retentionWarnings){
      warning.hidden=!validPricing||!t.minimumDays||e.deletionCharges<=0;
      warning.replaceChildren();
      if(!warning.hidden)warning.append(node('strong','Your retention settings incur early-deletion fees'),document.createTextNode(`${t.name} has a ${t.minimumDays}-day minimum storage duration. These settings prune revisions after about ${e.earliestPruneAgeDays} days, leaving ${t.minimumDays-e.earliestPruneAgeDays} unused days to pay for per deleted revision. Estimated fees: ${money(e.deletionCharges)} in the first year, already included in the totals. Keep backups longer or choose a storage class without a minimum duration to avoid these fees.`));
    }
    sourceLink.href=providers[p.provider].source;
    if(tierProvider!==p.provider){
      classChoices.replaceChildren();tierProvider=p.provider;
      tierRows=tiers.filter(c=>c.provider===p.provider).map(candidate=>{
        const choice=node('label',null,'cloud-class-choice'),radio=node('input'),description=node('small');
        radio.type='radio';radio.name='cloud-tier';radio.value=candidate.id;radio.setAttribute('aria-label',`${providers[p.provider].name} · ${candidate.name}`);
        description.id='cloud-class-'+candidate.id+'-description';radio.setAttribute('aria-describedby',description.id);
        radio.disabled=candidate.id==='azure-archive';radio.onchange=()=>{raw.tier=candidate.id;refresh();};
        choice.append(radio,node('span',candidate.name),description);classChoices.append(choice);return{candidate,radio,description};
      });
    }
    classHeading.textContent=`Storage class · ${validPricing?money(e.year):'—'} first year`;
    for(const{candidate,radio,description}of tierRows){
      radio.checked=candidate.id===p.tier;
      const cost=radio.disabled||!validPricing?null:candidate.id===p.tier?e:forecast(p,candidate.id);
      const price=cost?`${money(cost.firstMonth)} in ${cost.firstMonthLabel} (next full month) · ${money(cost.year)} first year. `:'';
      description.textContent=price+`${candidate.minimumDays?candidate.minimumDays+'-day minimum duration; early-deletion fees if pruned sooner. ':''}Recovery: ${candidate.latency.toLowerCase()}.${radio.disabled?' Unavailable for this setup.':''}`;
      const option=fields.tier.input.querySelector(`option[value="${candidate.id}"]`);
      option.textContent=`${candidate.name}${radio.disabled?' · unavailable':cost?' · '+money(cost.year)+' first year':''}`;
    }
    measurement.textContent=`${number(p.sizeGB)} GB ${measured?'measured library':'example library'} today → ${size(e.libraryYearBytes)} in one year. The cost forecast includes all retained compressed copies.`;
    fields.growthGBPerMonth.hint.textContent=p.growthBasis==='custom'?'Your custom growth estimate. Use the automatic estimate below to return to usage-based forecasting.':p.growthBasis==='measured_90_days'?`Measured from your Pharos usage over the last ${number(p.historyDays||90)} days, using the change in library size. Converted to a fixed monthly increase; it does not compound.`:`Only ${number(p.historyDays)} days of usage history are available; 90 days are needed to measure your growth rate. For now, the estimate uses 5% of today’s library size as a fixed monthly increase.`;
    growthNote.textContent=`${p.growthBasis==='custom'?'Your custom estimate':'The automatic estimate'} adds +${number(p.growthGBPerMonth)} GB to the library each month. The forecast uses a fixed increase, not compound growth.`;resetGrowth.hidden=p.growthBasis!=='custom';
    compressionNote.textContent=`The default assumes a compressed backup is 30% of the original library size, rounded from the Euclid library sample (${size(compressionMeasurement.uncompressed_bytes)} → ${size(compressionMeasurement.compressed_bytes)} with zstd -3). This is a fixed estimate, not a new compression measurement.`;
    configSummary.textContent=`${providers[p.provider].name} · ${frequency.toLowerCase()} · ${retention(p)}. ${number(p.sizeGB)} GB library, ${number(p.compression)}% compressed size, +${number(p.growthGBPerMonth)} GB/month.`;
    showErrors(errors);if(submitted&&!Object.keys(errors).length&&status.classList.contains('error'))message('');if(!card.hidden&&step<2)renderEstimate(comparison,!validPricing);
    const reviewFacts=[['Destination',p.name],['Provider and location',`${providers[p.provider].name} · ${p.region}`],['Bucket / container',p.provider==='azure'?`${p.account} / ${p.container}`:p.bucket],['Archive object',p.object],['Storage class',`${t.name} · ${validPricing?money(e.year):'—'} first year`],['Cloud account',p.provider==='aws'?`AWS profile ${p.profile}`:p.provider==='gcp'?`${p.project} (${p.projectMode==='new'?'create new project':'existing project'})`:p.subscription]];
    if(p.provider==='gcp'&&p.projectMode==='new')reviewFacts.push(['Billing account',p.billingAccount],['Project organization / folder',p.projectParent||'Personal project']);
    reviewFacts.push(['Schedule',schedule(p)],['Retention',retention(p)],['Upload speed','Use available connection speed'],['Expected first year',money(e.year)],['Credential and recovery',p.passwordProtected?'Credential in Pharos database · archive encryption · restore with cloud account and recovery password':'Credential in Pharos database · provider encryption · restore with cloud account']);
    facts(review,reviewFacts);codeDetails.hidden=p.passwordProtected;protectedCommands.hidden=!p.passwordProtected;copyCommands.hidden=p.passwordProtected;
    if(step===2){try{const stages=setupStages(raw);if(p.passwordProtected)commandStages.forEach((stage,i)=>stage.pre.textContent=stages[i].commands);else code.textContent=stages[0].commands;}catch(error){code.textContent=error.message;for(const stage of commandStages)stage.pre.textContent=error.message;}}
    for(const b of [copyCommands,copyPrompt,copyConfig,save,...commandStages.map(stage=>stage.copyButton)])b.disabled=Object.keys(errors).length>0||saving;save.textContent=saving?'Saving…':editing?'Update configuration in Pharos':'Save configuration to Pharos';renderOngoing();
  }
  async function persist(nextPlans){const response=await fetch('/api/preferences',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({set:{[prefKey]:nextPlans}})});if(!response.ok)throw new Error('Could not save configuration to the Pharos database.');saved=nextPlans;renderSaved();}
  async function savePlan(){if(Object.keys(validation(raw)).length)return;if(!editing&&saved.length>=20){message('This prototype supports up to 20 configured destinations.',true);return;}const id=editing||'cloud-'+Date.now().toString(36)+'-'+Math.random().toString(36).slice(2,7),plan={...normalize(raw),id},nextPlans=editing&&saved.some(p=>p.id===editing)?saved.map(p=>p.id===editing?plan:p):[...saved,plan];saving=true;refresh();try{await persist(nextPlans);editing=id;message('Configuration saved in Pharos. No credential imported and no upload scheduled.');}catch(error){message(error.message,true);}finally{saving=false;refresh();}}
  function renderSaved(){
    savedPanel.replaceChildren(node('h4','Configured destinations'));if(!saved.length&&!cloudState.destinations.some(d=>d.connected)){savedPanel.hidden=true;return;}savedPanel.hidden=false;let total=0;
    const plans=[...saved];for(const d of cloudState.destinations.filter(d=>d.connected))if(!plans.some(p=>connectedDestination(p)?.id===d.id))plans.push({...normalize(d.config),id:d.id});
    for(const plan of plans){const connected=connectedDestination(plan),activeSchedule=scheduleFor(connected),e=forecast(activeSchedule?.enabled?{...plan,...scheduleSpec(activeSchedule)}:plan),item=node('div',null,'cloud-saved-item');total+=e.year;item.append(node('strong',plan.name),node('p',`${providers[plan.provider].name} · ${plan.region} · ${money(e.year)} first year`,'cloud-note'),node('p',connected?`Connected · ${scheduleStatus(connected)}`:'Configured only · not connected','cloud-note'));const row=node('div',null,'cloud-actions');row.append(button('Edit',()=>{raw={...plan};editing=plan.id;editedSize=true;touched.clear();providerShown='';step=1;refresh();panels[1].focus();}),Object.assign(button(plan.passwordProtected?'View setup steps':'Copy commands',()=>{if(!plan.passwordProtected){copy(setup(plan));return;}raw={...plan};editing=plan.id;editedSize=true;touched.clear();submitted=false;step=2;refresh();panels[2].focus();}),{disabled:Object.keys(validation(plan)).length>0}),button('Remove',async()=>{try{const d=connectedDestination(plan);if(d)await cloudRequest('/disconnect',{destination_id:d.id});await persist(saved.filter(p=>p.id!==plan.id));await loadCloud();if(editing===plan.id)editing='';refresh();message('Configuration removed. Cloud resources were not changed.');}catch(error){message(error.message,true);}}));item.append(row);savedPanel.append(item);}
    savedPanel.append(node('p',`Combined first-year forecast: ${money(total)}.`,'cloud-note'),button('Plan another backup',()=>{const suffix=Math.random().toString(36).slice(2,8);raw={...normalize(raw),name:'Cloud backup',bucket:'pharos-backup-'+suffix,account:'pharos'+suffix,identity:'pharos-backup-'+suffix,group:'pharos-backup-'+suffix};editing='';step=0;submitted=false;touched.clear();providerShown='';refresh();mount.scrollIntoView({block:'start'});}));
  }
  window.pharosCloudBackup.seedLibrary=(bytes,growth)=>{
    latestGrowth=growth;if(bytes>0)latestLibraryBytes=bytes;if(editedSize||editing||!(bytes>0))return;
    const prior=JSON.stringify(normalize(raw));measured=true;raw.sizeGB=Math.max(.001,bytes/GB);if(raw.growthBasis!=='custom'){raw.growthBasis=growth?.basis||'fallback';raw.historyDays=growth?.history_days||0;raw.growthGBPerMonth=(growth?.bytes_per_day||0)*30/GB;}if(prior!==JSON.stringify(normalize(raw)))refresh();
  };
  const chartResizeObserver=new ResizeObserver(()=>{
    const width=Math.round(estimatePanel.clientWidth);
    if(!card.hidden&&step<2&&width>0&&width!==chartWidth)renderEstimate(projection(raw),Object.keys(validation(raw,0)).length>0);
  });chartResizeObserver.observe(estimatePanel);
  renderSaved();refresh();void loadCloud();
})();
