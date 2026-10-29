# Aturan kerja agen di repo ini

Berlaku untuk semua sesi agen (omp) di repo ini. Aturan ini hanya boleh diubah oleh pemilik repo.

## Hard gate — wajib persetujuan eksplisit

Agen **tidak boleh** menjalankan perintah berikut tanpa instruksi eksplisit dari user pada percakapan yang sedang berjalan:

| Perintah | Catatan |
|---|---|
| `git commit` | Termasuk commit lokal, sekecil apa pun |
| `git push`, `git push --force` | Selalu butuh persetujuan terpisah dari persetujuan commit |
| `git tag`, `gh release create/upload` | Termasuk tag yang memicu release CI |
| `git reset --hard`, `git rebase`, `git clean -fdx` | Menghilangkan pekerjaan |
| Menghapus/rename branch, menghapus file di luar perubahan yang diminta | — |
| Mengubah `.github/workflows/` yang memicu deploy/release | — |

Prosedur yang benar:

1. Selesaikan perubahan di working tree.
2. Jalankan verifikasi (test/lint/build yang relevan).
3. Tampilkan `git diff --stat` + ringkasan perubahan dan hasil verifikasi.
4. **Berhenti dan tunggu** persetujuan. Push hanya dilakukan jika user menyebut push/PR secara eksplisit.

Persetujuan sekali pakai: "commit" untuk perubahan itu tidak berarti izin push, dan tidak berlaku untuk perubahan berikutnya.

## Boleh tanpa persetujuan

- Membaca file, `git status/diff/log/show` (read-only).
- Mengedit/menambah file di working tree.
- Membuat branch baru dan berpindah ke branch baru tersebut.
- Menjalankan test, lint, build, dan server lokal.
- Menambah dependency (laporkan perubahan lockfile di ringkasan).
- Menjalankan migration pada database **lokal/dev** yang dibuat agen sendiri.

## Verifikasi

- Klaim "sudah dites" hanya jika perintahnya benar-benar dijalankan pada perubahan itu.
- Sertakan output ringkas (jumlah test lulus/gagal, error utama) di ringkasan.
- Jika test tidak bisa dijalankan (mis. butuh Postgres), katakan apa yang belum diverifikasi.

## Batasan lain

- Jangan pernah menulis secret/token ke repo atau ke log.
- Satu commit = satu perubahan logis.
- Jangan mengubah file milik user yang tidak terkait tugas (termasuk `.env`, data lokal, konfigurasi editor).
- Jika instruksi ambigu atau berisiko, berhenti dan tanya — jangan menebak.

## Riwayat

- 2026-10-29: dibuat atas permintaan pemilik repo; commit/push wajib persetujuan.
