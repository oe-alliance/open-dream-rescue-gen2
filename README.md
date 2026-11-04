# Open Dream Rescue 1.0 für Broadcom-Dreamboxen

Dieses Repository baut den internen Rescue Loader für DM900, DM920, DM7080, DM820 und die DM52x-Familie. Ein Build erzeugt das Rescue für genau ein Modell. DM520, DM525 und DM520mini verwenden gemeinsam das Ziel `dm520`. Die Gen1-Geräte wie DM7020 HD und die Amlogic-Modelle One/Two gehören zu anderen Loaderfamilien.

| ODR_MODEL | Plattform im OpenATV-Build | Kernel und Rescue-Format | Obergrenze |
|---|---|---|---:|
| dm900 / dm920 | OpenATV 8.1, dm7252s, ARMv7 Hardfloat | Linux 3.14, zImage mit initramfs | 14.417.920 Byte |
| dm7080 / dm820 | OpenATV 8.0, jeweils eigene Maschine, MIPS32 Little Endian | Linux 3.4, gzip-komprimiertes MIPS-ELF mit initramfs | 15.728.640 Byte |
| dm520 | OpenATV 8.0, dm520, MIPS32 Little Endian | Linux 3.4, gzip-komprimiertes MIPS-ELF mit initramfs | 13.631.488 Byte |

## Was hier gebaut wird

`backend/` enthält den Go-Webserver, den Image-Flasher, die Slot-/Backuplogik und die Weboberfläche. `hdmi/` enthält das C-Programm für HDMI, Dream-Fernbedienung und Frontdisplay. Der Server wird mit deaktiviertem cgo, die HDMI-UI statisch mit musl gebaut. Für die Bank-Container der DM7080/DM820 werden die mitgelieferten ofgwrite-Quellen unter `reference/ofgwrite/` übersetzt. `tools/pack.go` übernimmt die benötigte Basis aus dem modellspezifischen Werks-Rescue, ergänzt ODR und die Treiber und erstellt das initramfs. Der gepatchte OpenATV-Kernel wird mit diesem initramfs separat gelinkt. Der Build schreibt Dateien auf dem Linux-Host.

## 1. Linux-Buildhost vorbereiten

Benötigt werden ein x86-64-Linux mit einer eingerichteten OpenATV/OE-Buildumgebung, Go (erprobter Stand 1.25.14), GNU make, gcc, Perl, gzip, xz, tar, sha256sum, die üblichen Hostwerkzeuge für den jeweiligen Linux-Kernel sowie kmod mit `modinfo`, `depmod` und `modprobe`. Der Go-Backend-Build verwendet die Standardbibliothek. Klone das Repository auf ein Linux-Dateisystem und führe die Befehle als normaler Buildbenutzer im Repository-Stamm aus.

Im passenden OpenATV-Build zuerst die modellgerechten `linux-dreambox`- und `dreambox-dvb-modules-<modell>`-Rezepte bauen bzw. einen vorhandenen erfolgreichen Build verwenden. Benötigt werden der **gepatchte** Kernelquellbaum in `tmp/work-shared`, ein konfiguriertes Kernel-WORKDIR mit `build/.config` und `recipe-sysroot-native` (Cross-GCC und Binutils) und das Ziel-`libgcc` unter `tmp/sysroots-components`. Die ARM-Boards verwenden hier den eingerichteten dm920-WORKDIR im gemeinsamen dm7252s-Build. Die mitgelieferte Konfiguration in `configs/dm900/` bzw. `configs/dm920/` wählt das tatsächliche Board. Bei MIPS wird die Konfiguration des jeweiligen OE-WORKDIR verwendet.

## 2. Originaldateien bereitstellen

1. Lege das passende **unveränderte Werks-Rescue** unter `vendor/factory/` ab oder setze `ODR_FACTORY` auf dessen absoluten Pfad. Die exakten Dateinamen und SHA-256 stehen in [vendor/factory.sha256](vendor/factory.sha256). Für die **DM920** ist dies das ursprüngliche Rescue **#103** aus der Testbox. Das öffentliche `zImage-rescue-3.14-r0-dm920-20201201.bin` hat einen anderen Hash. Auch ein Dump der gesamten SPI-Partition hat eine andere Länge als der benötigte Kernel. Der Packer prüft die Eingabe gegen einen festen Modellhash.
2. Entpacke das Dream-Treiberpaket passend zur Kernel-ABI. `ODR_DRIVER_DIR` darf auf das Paket-Root mit `lib/modules/<release>/extra/` oder direkt auf `extra/` zeigen. Die zugehörige `LICENSE` muss neben den Modulen liegen. Benötigte Dream-Treiberstände sind **20200226** für dm900, **20200321** für dm920, **20190502** für dm7080, **20181018** für dm820 und **20180222** für dm520. `tools/driver-pack.sh` prüft Modellkennung, Versionsstring und vermagic.
3. Halte `musl-1.2.5.tar.gz` im Downloadarchiv bereit und setze `ODR_MUSL` auf den absoluten Dateipfad. Der Build erwartet SHA-256 `a9a118bbe84d8764da0ea0d28b3ab3fae8477fc7e4085d90102b8596fc7c75e4`.

Die Werksdateien liegen in der vorbereiteten lokalen Arbeitskopie außerhalb des Git-Index. Nach einem Klon müssen sie separat bereitgestellt werden. Sind alle fünf vorhanden, prüft dieser Befehl die Liste:

```sh
(cd vendor/factory && sha256sum -c ../factory.sha256)
```

## 3. Rescue bauen

Beispiel für eine **DM920** auf dem vorhandenen Builder. Die Pfade für den entpackten Treibersatz und das Downloadarchiv durch die eigenen ersetzen:

```sh
export ODR_MODEL=dm920
export OE_TMP=/home/oe1/atv8.1/build-enviroment/builds/openatv/release/dm7252s/tmp
export ODR_DRIVER_DIR=/pfad/zum/entpackten/dm920-treiberpaket
export ODR_MUSL=/pfad/zum/downloadarchiv/musl-1.2.5.tar.gz
export ODR_OUT="$HOME/odr-build/dm920-$(date +%Y%m%d-%H%M%S)"
export JOBS=8
sh tools/build-from-oe.sh
```

Für die **DM900** bleibt `OE_TMP` gleich. Wähle `ODR_MODEL=dm900`, den DM900-Treibersatz und ein eigenes Ausgabeverzeichnis. Für **MIPS** zeigt `OE_TMP` auf das individuelle Maschinenverzeichnis:

```sh
export ODR_MODEL=dm520                       # alternativ dm820 oder dm7080
export OE_TMP="/home/oe1/atv8.0/build-enviroment/builds/openatv/release/$ODR_MODEL/tmp"
export ODR_DRIVER_DIR="/pfad/zu/treibern/$ODR_MODEL"
export ODR_MUSL=/pfad/zum/downloadarchiv/musl-1.2.5.tar.gz
export ODR_OUT="$HOME/odr-build/$ODR_MODEL-$(date +%Y%m%d-%H%M%S)"
sh tools/build-from-oe.sh
```

Bei abweichender Verzeichnisstruktur überschreiben `ODR_KERNEL_WORK`, `ODR_KERNEL_SOURCE`, `ODR_KERNEL_CONFIG` und `ODR_APP_SYSROOT` die hergeleiteten Pfade. Das Zielverzeichnis muss leer sein. Nach einem abgebrochenen Build ein neues Verzeichnis wählen. Die Kernel-Objekte und die geänderte Kopie der Konfiguration landen unter `$ODR_OUT/kernel`.

### Separat mit BitBake bauen

Das beiliegende Offline-Rezept `recipes/odr-gen2_1.0.bb` verwendet denselben Modellbuild. Es benötigt dieselben externen Eingaben wie oben. Es lässt sich unabhängig von der normalen OpenATV-Imageproduktion starten. Nach dem Setzen von `ODR_MODEL`, `OE_TMP`, `ODR_DRIVER_DIR` und `ODR_MUSL`:

```sh
export BITBAKE_ROOT=/home/oe1/atv8.1/build-enviroment/bitbake
export ODR_BUILD=$(mktemp -d "$HOME/odr-gen2-bb.XXXXXXXX")
sh tools/run-bitbake.sh
(cd "$ODR_BUILD/deploy/$ODR_MODEL" && sha256sum -c ODR*.BIN.sha256)
```

Das Rezept legt seine eigenen Arbeitsdaten unter `$ODR_BUILD/tmp` an. Rescue-Datei, SHA-256 und der vorbereitete Ordner `bootmenu/` landen unter `$ODR_BUILD/deploy/$ODR_MODEL/`. Der Shell-Build verwendet stattdessen direkt `$ODR_OUT`.

## 4. Ergebnis prüfen und bereitstellen

```sh
(cd "$ODR_OUT" && sha256sum -c "ODR${ODR_MODEL#dm}.BIN.sha256")
cat "$ODR_OUT/bootmenu/$ODR_MODEL/rescue"
```

`ODR920.BIN`, `ODR900.BIN`, `ODR7080.BIN`, `ODR820.BIN` bzw. `ODR520.BIN` ist das vollständige Rescue. `odr.cpio.xz` allein ist nur das initramfs. Der Build prüft die Größenbeschränkung und die Modellkennung des fertigen Kernels. Unter `bootmenu/` liegen der Katalogeintrag und die zugehörige, mit einem Hash im Namen versehene Binärdatei für das Distributionsrepo. Beide gehören bei einer Veröffentlichung zusammen. Der gemeinsame Installer wird als eigenes Artefakt in `oe-alliance/bootmenu` gepflegt. Er wird beim Rescue-Build weder ausgeführt noch aktualisiert.

Zum schnellen Entwicklertest auf einem **Linux-Host** lassen sich die modellabhängigen Backendtests ohne Cross-Toolchain starten:

```sh
(cd backend && for model in dm520 dm7080 dm820 dm900 dm920; do
    GO111MODULE=off go test -tags "$model" . || exit 1
done)
```

Für eine echte Bootprüfung sind Modell, Bootloader und Medium relevant. Ein erfolgreicher Hosttest bestätigt weder HDMI/RCU noch das NAND- oder eMMC-Flashen. Informationen zur Installation und Bedienung auf dem Empfänger stehen in [docs/phpbb](docs/phpbb/).

## Typische Buildfehler

* **Wrong factory rescue base**: SHA-256 mit dem Eintrag für das Modell vergleichen, insbesondere bei DM920 #103. Die Prüfung schützt auch vor einem bereits installierten ODR als Eingabe.
* **Unsupported module ABI / fehlendes Modul**: Dream-Treiber und Kernel müssen denselben Modellnamen und Release-Suffix tragen. Bei ARM brauchen beide die Hardfloat-ABI, bei MIPS den richtigen BMIPS-Modulstand.
* **Build stoppt vor dem Compileraufruf**: In der gewählten Konfiguration müssen `CONFIG_DREAMBOX_<MODELL>=y` und `CONFIG_LOCALVERSION="-<modell>"` stehen. Prüfe außerdem die Cross-Toolchain im Kernel-WORKDIR.
* **Rescue zu groß**: Der Build nennt die Überschreitung in Byte. Erst wenn das Image in den Rescue-Bereich passt, wird der Katalog erstellt.
