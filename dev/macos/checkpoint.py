#!/usr/bin/env python3
"""Cross-process native checkpoint qualification; not a full Helmr Worker."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import stat
import sys

import probe

ROLES = {'state.vz': 768*probe.MIB, 'scratch.raw':64*probe.MIB,
         'computer.raw':64*probe.MIB, 'oracle.json':4096, 'machine-id':4096,
         'kernel':probe.INPUT_LIMIT, 'initramfs':probe.INPUT_LIMIT}
CONTRACT = 'helmr.macos-checkpoint-experiment.v1'
DEADLINE = None


def check_deadline():
    # Checked between chunks/phases. Filesystem syscalls are not preemptible;
    # helper processes separately enforce their own independent deadline.
    if DEADLINE is not None and probe.elapsed_clock() >= DEADLINE:
        raise probe.SafetyError('checkpoint experiment checked deadline exceeded')

CONFIG = {'cpu':1,'ram_mib':512,'disk_mib':[64,64], 'computer':'localversion-unix-nbd-fixture',
          'nic':'disconnected-filehandle','mac':'02:fc:00:00:00:02',
          'vsock':10789,'console':'virtio','binding':'Code-Hex/vz/v3@v3.8.0'}


def digest(path, limit):
    check_deadline()
    fd=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
    with os.fdopen(fd,'rb') as f:
        s=os.fstat(f.fileno())
        if not stat.S_ISREG(s.st_mode) or s.st_nlink!=1 or not 0<s.st_size<=limit:
            raise probe.SafetyError('invalid checkpoint object size/type')
        h=hashlib.sha256(); count=0
        while b:=f.read(probe.MIB):
            check_deadline()
            count+=len(b)
            if count>limit: raise probe.SafetyError('checkpoint object grew beyond bound')
            h.update(b)
        if count!=s.st_size: raise probe.SafetyError('checkpoint object changed size')
        return {'bytes':count,'sha256':h.hexdigest()}


def read_bounded(path,limit):
    check_deadline()
    fd=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
    with os.fdopen(fd,'rb') as f:
        info=os.fstat(f.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_size>limit:
            raise probe.SafetyError('invalid bounded file')
        raw=f.read(limit+1)
        if len(raw)>limit:raise probe.SafetyError('file exceeded bound')
        return raw


def verify_disk_oracle(folder,oracle):
    sequence=oracle['sequence']
    if type(sequence) is not int or not 1<=sequence<=3 or oracle['boot_count']!=1:
        raise probe.SafetyError('invalid disk oracle')
    for name in ('scratch.raw','computer.raw'):
        fd=os.open(folder/name,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
        with os.fdopen(fd,'rb') as f:
            if not stat.S_ISREG(os.fstat(f.fileno()).st_mode):raise probe.SafetyError('disk must be regular')
            if int.from_bytes(f.read(8),'little')!=1:raise probe.SafetyError('disk cold-boot count changed')
            f.seek(4096)
            for index in range(1,sequence+1):
                record=f.read(64)
                if (len(record)!=64 or int.from_bytes(record[:8],'little')!=index
                        or hashlib.sha256(record[:32]).digest()!=record[32:]):
                    raise probe.SafetyError('host disk journal missing, reordered or corrupt')


def copy_exact(source,target,limit):
    before=digest(source,limit)
    fd=os.open(source,os.O_RDONLY|os.O_NOFOLLOW)
    with os.fdopen(fd,'rb') as src,os.fdopen(probe.private_file(target),'wb') as dst:
        remaining=before['bytes']
        while remaining:
            check_deadline()
            b=src.read(min(probe.MIB,remaining))
            if not b: raise probe.SafetyError('truncated object')
            dst.write(b);remaining-=len(b)
        if src.read(1): raise probe.SafetyError('object grew')
        dst.flush();os.fsync(dst.fileno())
    if digest(target,limit)!=before: raise probe.SafetyError('copied object digest mismatch')
    return before


def seal(volume,identity,oracle):
    destination=volume/'checkpoint';destination.mkdir(mode=0o700)
    probe.write_record(volume/'oracle.json',oracle)
    objects={}
    for name,limit in ROLES.items():
        objects[name]=copy_exact(volume/name,destination/name,limit)
        (destination/name).chmod(0o400)
    manifest={'contract':CONTRACT,'identity':identity,'config':CONFIG,'objects':objects}
    probe.write_record(destination/'manifest.json',manifest)
    (destination/'manifest.json').chmod(0o400)
    probe.sync_directory(destination)
    return manifest


def validate(volume,identity):
    folder=volume/'checkpoint'
    raw=read_bounded(folder/'manifest.json',4096)
    if len(raw)>4096: raise probe.SafetyError('manifest too large')
    def unique(pairs):
        d={}
        for k,v in pairs:
            if k in d:raise probe.SafetyError('duplicate manifest key')
            d[k]=v
        return d
    m=json.loads(raw,object_pairs_hook=unique)
    if (not isinstance(m,dict) or set(m)!={'contract','identity','config','objects'}
        or m['contract']!=CONTRACT or m['identity']!=identity or m['config']!=CONFIG
        or not isinstance(m['objects'],dict) or set(m['objects'])!=set(ROLES)):
        raise probe.SafetyError('checkpoint schema or compatibility mismatch')
    for name,limit in ROLES.items():
        if digest(folder/name,limit)!=m['objects'][name]:
            raise probe.SafetyError('checkpoint digest mismatch')
    for name in ('kernel','initramfs','machine-id'):
        if digest(volume/name,ROLES[name])!=m['objects'][name]:
            raise probe.SafetyError('live configuration differs from saved cut')
    return m


def run(args):
    global DEADLINE
    started=probe.elapsed_clock()
    DEADLINE=started+180
    if sys.platform!='darwin' or platform.machine()!='arm64' or os.geteuid()==0:
        raise probe.SafetyError('unprivileged Apple Silicon macOS required')
    if not shutil.rmtree.avoids_symlink_attacks:raise probe.SafetyError('safe cleanup required')
    helper=probe.HERE/'.build'/'helmr-vz-probe'
    helper_identity=digest(helper,32*probe.MIB)
    identity={'uid':os.getuid(),'os':platform.mac_ver()[0],
              'model':probe.bounded_command(['/usr/sbin/sysctl','-n','hw.model'],seconds=3).decode().strip(),
              'host':probe.bounded_command(['/usr/sbin/sysctl','-n','kern.bootsessionuuid'],seconds=3).decode().strip(),
              'helper':helper_identity}
    probe.host_headroom(probe.ROOT.parent,probe.HOST_DISK_RESERVE+probe.IMAGE_LIMIT)
    with probe.Admission() as admission:
        def command(cmd,**kw):
            check_deadline()
            kw['seconds']=min(kw.get('seconds',30),DEADLINE-probe.elapsed_clock())
            return probe.bounded_command(cmd,pass_fds=(admission.fd,),cwd=admission.run,**kw)
        image=probe.Image(admission.run,command);image.create_attach()
        for name in ('scratch.raw','computer.raw'):
            with os.fdopen(probe.private_file(image.mount/name),'wb') as f:f.truncate(64*probe.MIB)
        probe.copy_pinned(args.kernel,image.mount/'kernel',args.kernel_sha256)
        probe.copy_pinned(args.initramfs,image.mount/'initramfs',args.initramfs_sha256)
        samples=[];last=[0.0]
        def monitor():
            now=probe.elapsed_clock()
            check_deadline()
            if now-last[0]>=1:
                samples.append(probe.host_headroom(admission.run));last[0]=now
                if image.path.stat().st_size>probe.IMAGE_LIMIT:raise probe.SafetyError('image bound exceeded')
        def invoke(mode):
            if digest(helper,32*probe.MIB)!=helper_identity:raise probe.SafetyError('helper changed')
            raw=command([str(helper),mode,str(image.mount),str(admission.fd)],seconds=35,tick=monitor)
            report=json.loads(raw)
            probe.write_record(admission.run/(mode+'.json'),report)
            if report.get('mode')!=mode or report.get('clean') is not True or report.get('state')!='stopped':
                raise probe.SafetyError('helper did not establish clean stop')
            if (report.get('disk')!='localversion-private-unix-nbd'
                    or report.get('localversion_flush_reopen_verified') is not True):
                raise probe.SafetyError('helper did not establish LocalVersion reopen')
            return report
        saved=invoke('checkpoint-save')
        if saved.get('online_cut_preserved_after_later_flush') is not True:
            raise probe.SafetyError('online capture verification missing')
        verify_disk_oracle(image.mount,saved['frozen'])
        # bounded_command has reaped the original helper; no second owner overlaps.
        probe.write_record(admission.run/'source-excluded.json',{'source_exited':True})
        sealed=seal(image.mount,identity,saved['frozen']);monitor()
        validate(image.mount,identity)
        restored=image.mount/'restore';restored.mkdir(mode=0o700)
        for name in ('scratch.raw','computer.raw'):
            copy_exact(image.mount/'checkpoint'/name,restored/name,ROLES[name])
        probe.sync_directory(restored)
        resumed=invoke('checkpoint-restore')
        verify_disk_oracle(restored,resumed['continued'])
        if saved['frozen']!=resumed['frozen'] or resumed.get('restored_paused') is not True:
            raise probe.SafetyError('restore comparison failed')
        # The immutable cut must survive continuation unchanged.
        validate(image.mount,identity)
        image.detach();check_deadline();admission.complete();check_deadline()
    return {'checkpoint_cycle_verified':True,'worker_verified':False,'saved':saved,'restored':resumed,
            'identity':identity,'manifest':sealed,'cleanup':'both helpers reaped; exact image detached; private artifacts removed',
            'duration_seconds':probe.elapsed_clock()-started,'host_samples':samples}


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    for name in ('kernel','initramfs'):
        parser.add_argument('--'+name,type=Path,required=True)
        parser.add_argument('--'+name+'-sha256',required=True)
    args=parser.parse_args()
    try: print(json.dumps(run(args),indent=2,sort_keys=True))
    except Exception as error:
        state=(f'Quarantine retained under {probe.ROOT}' if (probe.ROOT/'active.json').exists()
               else 'No unresolved run marker; inspect the error before retrying')
        print(f'Checkpoint not qualified: {error}. {state}',file=sys.stderr)
        raise SystemExit(1)
