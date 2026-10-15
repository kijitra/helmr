import copy
import json
from pathlib import Path
import tempfile
import unittest

import checkpoint
import probe


class CheckpointTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name)
        self.identity={'uid':501,'host':'test','helper':{'sha256':'a'*64}}
        for name in checkpoint.ROLES:
            if name!='oracle.json':(self.root/name).write_bytes((name+' test\n').encode())
        self.manifest=checkpoint.seal(self.root,self.identity,{'nonce':'never-send-to-guest'})

    def edit_manifest(self, mutate):
        p=self.root/'checkpoint'/'manifest.json';p.chmod(0o600)
        m=copy.deepcopy(self.manifest);mutate(m);p.write_text(json.dumps(m))

    def test_valid_cut(self):
        self.assertEqual(checkpoint.validate(self.root,self.identity),self.manifest)

    def test_every_object_corruption_rejected(self):
        for role in checkpoint.ROLES:
            with self.subTest(role=role):
                p=self.root/'checkpoint'/role;p.chmod(0o600);raw=p.read_bytes();p.write_bytes(raw[:-1]+b'!')
                with self.assertRaises(probe.SafetyError):checkpoint.validate(self.root,self.identity)
                p.write_bytes(raw)

    def test_truncated_state_rejected(self):
        p=self.root/'checkpoint'/'state.vz';p.chmod(0o600);p.write_bytes(b'')
        with self.assertRaises(probe.SafetyError):checkpoint.validate(self.root,self.identity)

    def test_foreign_host_or_helper_rejected(self):
        for name,value in [('host','different'),('helper',{'sha256':'b'*64})]:
            identity=copy.deepcopy(self.identity);identity[name]=value
            with self.assertRaises(probe.SafetyError):checkpoint.validate(self.root,identity)

    def test_unknown_version_field_role_or_shape_rejected(self):
        changes=[lambda m:m.update(contract='future'),lambda m:m.update(extra=True),
                 lambda m:m['objects'].update(extra={}),lambda m:m['config'].update(cpu=2),
                 lambda m:m['objects'].pop('scratch.raw')]
        for change in changes:
            self.edit_manifest(change)
            with self.assertRaises(probe.SafetyError):checkpoint.validate(self.root,self.identity)

    def test_live_kernel_mismatch_rejected(self):
        (self.root/'kernel').write_bytes(b'changed')
        with self.assertRaises(probe.SafetyError):checkpoint.validate(self.root,self.identity)

    def test_writable_restore_copy_does_not_modify_cut(self):
        target=self.root/'restored.raw'
        checkpoint.copy_exact(self.root/'checkpoint'/'computer.raw',target,4096)
        target.write_bytes(b'guest writes')
        checkpoint.validate(self.root,self.identity)

    def test_duplicate_manifest_fields_rejected(self):
        p=self.root/'checkpoint'/'manifest.json';p.chmod(0o600)
        raw=p.read_text();p.write_text('{"contract":"bad",'+raw[1:])
        with self.assertRaises(probe.SafetyError):checkpoint.validate(self.root,self.identity)

    def test_symlink_object_rejected(self):
        p=self.root/'checkpoint'/'computer.raw';p.unlink();p.symlink_to(self.root/'computer.raw')
        with self.assertRaises(OSError):checkpoint.validate(self.root,self.identity)

    def test_oversize_input_rejected_before_copy(self):
        p=self.root/'large';p.write_bytes(b'12345')
        with self.assertRaises(probe.SafetyError):checkpoint.copy_exact(p,self.root/'copy',4)
        self.assertFalse((self.root/'copy').exists())



class DiskOracleTests(unittest.TestCase):
    def test_guest_cache_cannot_mask_lost_or_corrupt_disk_write(self):
        import hashlib
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            for name in ('scratch.raw','computer.raw'):
                b=bytearray(8192);b[:8]=(1).to_bytes(8,'little')
                for seq in (1,2):
                    record=bytearray(64);record[:8]=seq.to_bytes(8,'little')
                    record[32:]=hashlib.sha256(record[:32]).digest()
                    offset=4096+(seq-1)*64;b[offset:offset+64]=record
                (root/name).write_bytes(b)
            oracle={'sequence':2,'boot_count':1}
            checkpoint.verify_disk_oracle(root,oracle)
            for name in ('scratch.raw','computer.raw'):
                p=root/name;original=p.read_bytes();bad=bytearray(original);bad[4160:4224]=bytes(64);p.write_bytes(bad)
                with self.assertRaises(probe.SafetyError):checkpoint.verify_disk_oracle(root,oracle)
                p.write_bytes(original)

    def test_oversize_manifest_is_rejected_before_read(self):
        with tempfile.TemporaryDirectory() as tmp:
            p=Path(tmp)/'manifest';p.write_bytes(b'x'*4097)
            with self.assertRaises(probe.SafetyError):checkpoint.read_bounded(p,4096)

class DeadlineTests(unittest.TestCase):
    def test_expired_deadline_rejects_before_file_access(self):
        from unittest.mock import patch
        with patch.object(checkpoint,'DEADLINE',10),patch.object(probe,'elapsed_clock',return_value=11):
            with self.assertRaises(probe.SafetyError):checkpoint.digest(Path('/does-not-exist'),4096)
            with self.assertRaises(probe.SafetyError):checkpoint.read_bounded(Path('/does-not-exist'),4096)

if __name__=='__main__':unittest.main()
