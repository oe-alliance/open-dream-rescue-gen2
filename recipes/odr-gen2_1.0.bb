SUMMARY = "Open Dream Rescue for Broadcom Gen2 receivers"
LICENSE = "CLOSED & GPL-2.0-or-later & GPL-3.0-only & Apache-2.0"
EXCLUDE_FROM_WORLD = "1"

# httptest uses local TCP listeners. Keep the compile task network-isolated.
do_check_backend[network] = "1"
do_check_backend[nostamp] = "1"
do_check_backend() {
    cd "${ODR_SOURCE}/backend"
    GO111MODULE=off GOPROXY=off GOTOOLCHAIN=local go test -tags "${ODR_MODEL}" .
}

# The configured external OE toolchain, kernel and driver tree can change.
# Build afresh for each invocation, using the same entry point as the shell build.
do_compile[nostamp] = "1"
do_compile[cleandirs] = "${B}"
do_compile() {
    OE_TMP="${OE_TMP}" ODR_MODEL="${ODR_MODEL}" ODR_DRIVER_DIR="${ODR_DRIVER_DIR}" \
    ODR_MUSL="${ODR_MUSL}" ODR_FACTORY="${ODR_FACTORY}" ODR_OUT="${B}" \
    ODR_KERNEL_WORK="${ODR_KERNEL_WORK}" ODR_KERNEL_SOURCE="${ODR_KERNEL_SOURCE}" \
    ODR_KERNEL_CONFIG="${ODR_KERNEL_CONFIG}" ODR_APP_SYSROOT="${ODR_APP_SYSROOT}" \
    ODR_BACKEND_TESTED=1 JOBS="${JOBS}" sh "${ODR_SOURCE}/tools/build-from-oe.sh"
}

ODR_FACTORY ?= ""
ODR_KERNEL_WORK ?= ""
ODR_KERNEL_SOURCE ?= ""
ODR_KERNEL_CONFIG ?= ""
ODR_APP_SYSROOT ?= ""
JOBS ?= "4"
do_deploy[dirs] = "${DEPLOY_DIR_IMAGE}"
do_deploy() {
    cp "${B}"/ODR*.BIN "${B}"/ODR*.BIN.sha256 "${DEPLOY_DIR_IMAGE}/"
    cp -a "${B}/bootmenu" "${DEPLOY_DIR_IMAGE}/"
    cd "${DEPLOY_DIR_IMAGE}"
    sha256sum -c ODR*.BIN.sha256
}
addtask check_backend before do_compile
addtask compile after do_check_backend before do_deploy
addtask deploy after do_compile before do_build
