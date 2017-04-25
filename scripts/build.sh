#!/usr/bin/env bash
set -e

COZY_ENV_DFL=production

[ -z "${COZY_ENV}" ] && COZY_ENV="${COZY_ENV_DFL}"
[ -z "${COZY_DEPLOY_USER}" ] && COZY_DEPLOY_USER="${USER}"

pushd "$(dirname "$0")" > /dev/null
WORK_DIR=$(dirname "$(pwd)")
popd > /dev/null

if [ -r "${WORK_DIR}/local.env" ]; then
	. "${WORK_DIR}/local.env"
fi

echo_err() {
	>&2 echo -e "ERR: ${1}"
}

echo_wrn() {
	>&2 echo -e "WRN: ${1}"
}

usage() {
	echo -e "Usage: ${1} [release] [install] [deploy] [docker-dev] [assets] [clean]"
	echo -e "\nCommands:\n"
	echo -e "  release     builds a release of the current working-tree"
	echo -e "  install     builds a release and install it the GOPATH"
	echo -e "  deploy      builds a release of the current working-tree and deploys it"
	echo -e "  docker-dev  builds a cozy-app-dev docker image"
	echo -e "  assets      move and download all the required assets (see: ./assets/externals)"
	echo -e "  clean       remove all generated files from the working-tree"

	echo -e "\nEnvironment variables:"
	echo -e "\n  COZY_ENV"
	echo -e "    with release command, specify the environment of the release."
	echo -e "    can be \"production\" or \"development\". default: \"${COZY_ENV_DFL}\""
	echo -e "\n  COZY_DEPLOY_USER"
	echo -e "    with deploy command, specify the user used to deploy."
	echo -e "    default: \$USER (${USER})"
	echo -e "\n  COZY_DEPLOY_SERVER"
	echo -e "    with deploy command, specify the ssh server to deploy on."
	echo -e "\n  COZY_DEPLOY_PROXY"
	echo -e "    with deploy command, specify an ssh proxy to go through."
	echo -e "\n  COZY_DEPLOY_POSTSCRIPT"
	echo -e "    with deploy command, specify an optional script to execute"
	echo -e "    on the deploy server after deploying."
}

do_prepare_ldflags() {
	eval "$(go env)"

	VERSION_OS_ARCH="${GOOS}-${GOARCH}"
	VERSION_STRING=$(git --git-dir="${WORK_DIR}/.git" --work-tree="${WORK_DIR}" \
		describe --tags --dirty 2> /dev/null | \
		sed -E 's/(.*)-g[[:xdigit:]]+(-?.*)$/\1\2/g')

	if [ "${VERSION_STRING}" == "" ]; then
		VERSION_STRING="v0-$(git --git-dir="${WORK_DIR}/.git" rev-parse --short HEAD)"
		echo_wrn "No tag has been found to version the stack, using \"${VERSION_STRING}\" as version number"
	fi

	if [ "$(git --git-dir="${WORK_DIR}/.git" diff --shortstat HEAD | wc -l)" -gt 0 ]; then
		if [ "${COZY_ENV}" == production ]; then
			echo_err "Can not build a production release in a dirty work-tree"
			exit 1
		fi
		VERSION_STRING="${VERSION_STRING}-dirty"
	fi

	if [ "${COZY_ENV}" == development ]; then
		VERSION_STRING="${VERSION_STRING}-dev"
	fi

	BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
	BUILD_MODE="${COZY_ENV}"
}

# The version string is deterministic and reflects entirely the state
# of the working-directory from which the release is built from. It is
# generated using the following format:
#
# 		<TAG>[-<NUMBER OF COMMITS AFTER TAG>][-dirty][-dev]
#
# Where:
#  - <TAG>: closest annotated tag of the current working directory. If
#    no tag is present, is uses the string "v0". This is not allowed
#    in a production release.
#  - <NUMBER OF COMMITS AFTER TAG>: number of commits after the
#    closest tag if the current working directory does not point
#    exactly to a tag
#  - -dirty: added if the working if the working-directory is not
#    clean (contains un-commited modifications). This is not allowed
#    in production release.
#  - -dev: added for a development mode relase
#
# The outputed binary is named "cozy-stack-${VERSION_STRING}". A
# SHA256 checksum of the binary is also generated in a file named
# "cozy-stack-${VERSION_STRING}.sha256".
do_release() {
	check_env

	do_build
	openssl dgst -sha256 -hex "${BINARY}" > "${BINARY}.sha256"

	printf "%s\t" "${BINARY}"
	sed -E 's/SHA256\((.*)\)= ([a-f0-9]+)$/\2/g' < "${BINARY}.sha256"
}

do_install() {
	check_env

	do_prepare_ldflags

	printf "installing cozy-stack in %s... " "${GOPATH}"
	go install -ldflags "\
		-X github.com/cozy/cozy-stack/pkg/config.Version=${VERSION_STRING} \
		-X github.com/cozy/cozy-stack/pkg/config.BuildTime=${BUILD_TIME} \
		-X github.com/cozy/cozy-stack/pkg/config.BuildMode=${BUILD_MODE}"
	echo "ok"
}

do_build() {
	check_env

	do_prepare_ldflags
	do_assets

	if [ -z "${1}" ]; then
		BINARY="cozy-stack-${VERSION_OS_ARCH}-${VERSION_STRING}"
	else
		BINARY="${1}"
	fi

	printf "building cozy-stack in %s... " "${BINARY}"
	go build -ldflags "\
		-X github.com/cozy/cozy-stack/pkg/config.Version=${VERSION_STRING} \
		-X github.com/cozy/cozy-stack/pkg/config.BuildTime=${BUILD_TIME} \
		-X github.com/cozy/cozy-stack/pkg/config.BuildMode=${BUILD_MODE}
		" \
		-o "${BINARY}"
	echo "ok"
}

# The deploy command will build a new release and deploy it on a
# distant server using scp. To configure the distance server, you can
# use the environment variables (see help usage):
#
#  - COZY_DEPLOY_USER: deploy user (default to $USER)
#  - COZY_DEPLOY_SERVER: deploy server
#  - COZY_DEPLOY_PROXY: deploy proxy (optional)
#  - COZY_DEPLOY_POSTSCRIPT: deploy script to execute after deploy
#    (optional)
#
do_deploy() {
	check_env

	do_release

	if [ -z "${COZY_DEPLOY_PROXY}" ]; then
		scp "${BINARY}" "${COZY_DEPLOY_USER}@${COZY_DEPLOY_SERVER}:cozy-stack"
	else
		scp -oProxyCommand="ssh -W %h:%p ${COZY_DEPLOY_PROXY}" "${BINARY}" "${COZY_DEPLOY_USER}@${COZY_DEPLOY_SERVER}:cozy-stack"
	fi

	if [ -n "${COZY_DEPLOY_POSTSCRIPT}" ]; then
		if [ -z "${COZY_DEPLOY_PROXY}" ]; then
			ssh "${COZY_DEPLOY_USER}@${COZY_DEPLOY_SERVER}" "${COZY_DEPLOY_POSTSCRIPT}"
		else
			ssh "${COZY_DEPLOY_PROXY}" ssh "${COZY_DEPLOY_USER}@${COZY_DEPLOY_SERVER}" "${COZY_DEPLOY_POSTSCRIPT}"
		fi
	fi

	rm "${BINARY}"
	rm "${BINARY}.sha256"
}

do_assets() {
	clean_assets
	tx pull -a || echo "Do you have configured transifex?"
	prepare_assets
	printf "executing go generate... "
	go get -u github.com/rakyll/statik
	rm -f "${WORK_DIR}/web/statik/statik.go"
	go generate ./web
	echo "ok"
	clean_assets
}

do_docker_dev_image() {
	docker_work_dir="${WORK_DIR}/.docker-work"
	mkdir "${docker_work_dir}"

	trap 'trap - SIGTERM && rm -rf "${docker_work_dir}" > /dev/null -- -${$}' SIGINT SIGTERM EXIT

	cp "${WORK_DIR}/scripts/Dockerfile" "${docker_work_dir}"
	cp "${WORK_DIR}/scripts/docker-entrypoint.sh" "${docker_work_dir}"
	cp "${WORK_DIR}/scripts/cozy-app-dev.sh" "${docker_work_dir}"

	export GOOS=linux
	export GOARCH=amd64
	export COZY_ENV=development
	do_build "${docker_work_dir}/cozy-stack"

	docker build -t cozy/cozy-app-dev "${docker_work_dir}"

	rm -rf "${docker_work_dir}"
}

clean_assets() {
	rm -rf "${WORK_DIR}/.assets"
}

prepare_assets() {
	assets_dst="${WORK_DIR}/.assets"
	assets_src="${WORK_DIR}/assets"
	assets_externals="${assets_src}/externals"

	if [ "$(git --git-dir="${WORK_DIR}/.git" diff --shortstat HEAD -- "${assets_externals}" | wc -l)" -gt 0 ]; then
		echo_err "file ${assets_externals} is dirty."
		echo_err "it should be commited into git in order to generate the asset file."
		exit 1
	fi

	assets_mod_time=$(git log --pretty=format:%cd -n 1 --date=iso "${assets_externals}")
	case "$(uname -s)" in
		Darwin)
			assets_mod_time=$(date -j -f '%Y-%m-%d %H:%M:%S %z' "${assets_mod_time}" +%Y%m%d%H%M.%S)
			;;
		*)
			assets_mod_time=$(date -d "${assets_mod_time}" +%Y%m%d%H%M.%S)
			;;
	esac

	mkdir "${assets_dst}"

	asset_name=""
	asset_url=""
	asset_sha=""
	while IFS= read -r line; do
		if [ "${line:0:1}" = "#" ]; then
			continue
		fi

		if [ -z "${line}" ]; then
			[ -n "${asset_name}" ] && download_asset "${asset_name}" "${asset_url}" "${asset_sha}"
			asset_name=""
			asset_url=""
			asset_sha=""
			continue
		fi

		IFS=" " read -r -a line_split <<< "${line}"
		case "${line_split[0]}" in
			name)
				asset_name="${line_split[1]}"
				;;
			url)
				asset_url="${line_split[1]}"
				;;
			sha256)
				asset_sha="${line_split[1]}"
				;;
			*)
				echo_err "Failed to parse ${assets_src}/externals file"
				echo_err "Unknown field named \"${line_split[0]}\""
				exit 1
				;;
		esac
	done < "${assets_externals}"

	[ -n "${asset_name}" ] && download_asset "${asset_name}" "${asset_url}" "${asset_sha}"

	cp -a "${assets_src}/." "${assets_dst}"
	rm -f "${assets_dst}/externals"
}

download_asset() {
	echo "${1}:"
	mkdir -p "${assets_dst}/${1%/*}"
	printf "\tdownloading %s... " "${1}"
	set +e
	curl -s --fail "${2}" > "${assets_dst}/${1}"
	retc=${?}
	set -e
	if [ ${retc} -ne 0 ]; then
		echo "failed"
		echo_err "Could not fetch resource ${2}"
		echo_err "curl failed with return code ${retc}"
		exit 1
	fi
	echo "ok"
	if [ -n "${3}" ]; then
		printf "\tchecking sha256... "
		dgst=$(openssl dgst -sha256 < "${assets_dst}/${1}" | sed 's/^.* //')
		if [ "${3}" != "${dgst}" ]; then
			echo "failed"
			echo_err "Checksum SHA256 does not match for asset ${1} downloaded on ${2}"
			exit 1
		fi
		echo "ok"
	fi
	du -h "${assets_dst}/${1}"
	# reuse the same mod time properties as the externals files so have
	# reproductible output
	touch -m -t "${assets_mod_time}" "${assets_dst}/${1}"
}

do_clean() {
	find "${WORK_DIR}" -name "cozy-stack-*" -print -delete
	clean_assets
}

check_env() {
	if [ "${COZY_ENV}" != "production" ] && [ "${COZY_ENV}" != "development" ]; then
		echo_err "ERR: COZY_ENV should either be production or development"
		exit 1
	fi
}

case "${1}" in
	release)
		do_release
		;;

	install)
		do_install
		;;

	deploy)
		do_deploy
		;;

	clean)
		do_clean
		;;

	assets)
		do_assets
		;;

	docker-dev)
		do_docker_dev_image
		;;

	*)
		usage "${0}"
		exit 1
esac

exit 0
